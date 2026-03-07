package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"recents/internal/storage"
)

type inputMode int

const (
	modeNormal inputMode = iota
	modeSearch
	modeFilter
)

type queryResultMsg struct {
	records []storage.FileRecord
	err     error
	dur     time.Duration
	id      int
}

type actionResultMsg struct {
	err error
}

type missingCheckMsg struct {
	byID map[int64]bool
}

type clearStatusMsg struct{}
type heartbeatMsg time.Time

type model struct {
	store *storage.Store

	records []storage.FileRecord
	err     error

	width  int
	height int

	cursor int
	top    int

	mode inputMode

	search       string
	searchInput  string
	filter       string
	filterInput  string
	status       string
	statusErr    bool
	queryLimit   int
	refreshing   bool
	lastQueryDur time.Duration
	startedAt    time.Time
	startupDur   time.Duration
	lastQueryID  int
	tickCount    int
}

func newModel(store *storage.Store) model {
	return model{
		store:      store,
		queryLimit: 500,
		refreshing: true,
		startedAt:  time.Now(),
	}
}

func (m model) Init() tea.Cmd {
	m.lastQueryID++
	return tea.Batch(
		m.refreshCmd(m.lastQueryID),
		heartbeatCmd(),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ensureCursorVisible()
		return m, nil

	case queryResultMsg:
		if msg.id != m.lastQueryID {
			return m, nil
		}
		m.refreshing = false
		m.records = msg.records
		m.err = msg.err
		m.lastQueryDur = msg.dur
		if m.startupDur == 0 {
			m.startupDur = time.Since(m.startedAt)
		}
		if msg.err != nil {
			m.setStatusErr(msg.err)
		} else if msg.dur > 20*time.Millisecond {
			m.setStatus(fmt.Sprintf("Query slower than target: %s (>20ms)", msg.dur.Round(time.Millisecond)))
		} else {
			m.setStatus("")
		}
		if m.cursor >= len(m.records) {
			m.cursor = max(0, len(m.records)-1)
		}
		m.ensureCursorVisible()
		return m, checkMissingCmd(m.records)

	case actionResultMsg:
		if msg.err != nil {
			m.setStatusErr(msg.err)
		} else {
			m.setStatus("Action completed")
		}
		return m, clearStatusAfterDelay()

	case missingCheckMsg:
		for i := range m.records {
			m.records[i].Missing = msg.byID[m.records[i].ID]
		}
		return m, nil

	case clearStatusMsg:
		if !m.refreshing {
			m.setStatus("")
		}
		return m, nil

	case heartbeatMsg:
		m.tickCount++
		cmds := []tea.Cmd{heartbeatCmd()}
		// Poll DB periodically so new opens appear without manual refresh.
		if m.tickCount%2 == 0 && !m.refreshing {
			m.refreshing = true
			m.lastQueryID++
			cmds = append(cmds, m.refreshCmd(m.lastQueryID))
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		if m.mode != modeNormal {
			return m.handleInputModeKey(msg)
		}
		return m.handleNormalModeKey(msg)
	}

	return m, nil
}

func (m model) handleInputModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
		m.ensureCursorVisible()
		return m, nil
	case tea.KeyDown:
		if m.cursor < len(m.records)-1 {
			m.cursor++
		}
		m.ensureCursorVisible()
		return m, nil
	case tea.KeyEsc:
		if m.mode == modeFilter {
			m.filter = ""
			m.filterInput = ""
		}
		m.mode = modeNormal
		m.refreshing = true
		m.lastQueryID++
		return m, m.refreshCmd(m.lastQueryID)
	case tea.KeyEnter:
		// Live mode already applies query while typing. Enter just exits edit mode.
		m.mode = modeNormal
		return m, nil
	case tea.KeyBackspace, tea.KeyDelete:
		if m.mode == modeSearch {
			m.searchInput = trimLastRune(m.searchInput)
			m.search = strings.TrimSpace(m.searchInput)
		} else {
			m.filterInput = trimLastRune(m.filterInput)
			m.filter = strings.TrimSpace(m.filterInput)
		}
		m.refreshing = true
		m.lastQueryID++
		return m, m.refreshCmd(m.lastQueryID)
	case tea.KeyCtrlC:
		return m, tea.Quit
	default:
		switch msg.String() {
		case "j":
			if m.cursor < len(m.records)-1 {
				m.cursor++
			}
			m.ensureCursorVisible()
			return m, nil
		case "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.ensureCursorVisible()
			return m, nil
		}
		if msg.Type == tea.KeyRunes {
			if m.mode == modeSearch {
				m.searchInput += string(msg.Runes)
				m.search = strings.TrimSpace(m.searchInput)
			} else {
				m.filterInput += string(msg.Runes)
				m.filter = strings.TrimSpace(m.filterInput)
			}
			m.refreshing = true
			m.lastQueryID++
			return m, m.refreshCmd(m.lastQueryID)
		}
		return m, nil
	}
}

func (m model) handleNormalModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.records)-1 {
			m.cursor++
		}
		m.ensureCursorVisible()
		return m, nil
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
		m.ensureCursorVisible()
		return m, nil
	case "g":
		m.cursor = 0
		m.ensureCursorVisible()
		return m, nil
	case "G":
		if len(m.records) > 0 {
			m.cursor = len(m.records) - 1
		}
		m.ensureCursorVisible()
		return m, nil
	case "r":
		fallthrough
	case "R":
		fallthrough
	case "u":
		fallthrough
	case "ctrl+r":
		m.refreshing = true
		m.lastQueryID++
		return m, m.refreshCmd(m.lastQueryID)
	case "/":
		m.mode = modeSearch
		m.searchInput = m.search
		return m, nil
	case "f":
		m.mode = modeFilter
		m.filterInput = m.filter
		return m, nil
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.filterInput = ""
			m.refreshing = true
			m.lastQueryID++
			return m, m.refreshCmd(m.lastQueryID)
		}
		return m, nil
	case "enter":
		rec, ok := m.currentRecord()
		if !ok {
			return m, nil
		}
		if rec.Missing {
			m.setStatusErr(fmt.Errorf("file no longer exists: %s", rec.Path))
			return m, nil
		}
		return m, openExistingPathCmd(rec.Path, false)
	case "ctrl+o":
		rec, ok := m.currentRecord()
		if !ok {
			return m, nil
		}
		if rec.Missing {
			m.setStatusErr(fmt.Errorf("directory no longer exists: %s", rec.Directory))
			return m, clearStatusAfterDelay()
		}
		return m, openExistingPathCmd(rec.Directory, true)
	case "ctrl+c":
		return m, tea.Quit
	default:
		return m, nil
	}
}

func (m model) View() string {
	title := lipgloss.NewStyle().Bold(true).Render("Recents")
	searchValue := m.search
	if m.mode == modeSearch {
		searchValue = m.searchInput + "_"
	}
	filterValue := m.filter
	if m.mode == modeFilter {
		filterValue = m.filterInput + "_"
	}
	if searchValue == "" {
		searchValue = "(none)"
	}
	if filterValue == "" {
		filterValue = "(none)"
	}

	meta := fmt.Sprintf("Search: %s   Filter: %s", searchValue, filterValue)
	metaLine := lipgloss.NewStyle().Faint(true).Render(meta)

	list := m.renderList()

	help := "j/k,↑/↓ move  g/G top/bottom  Enter open  Ctrl+o folder  / search  f filter  Esc clear filter  r/u/R/Ctrl+r refresh  q quit"
	helpLine := lipgloss.NewStyle().Faint(true).Render(help)

	status := m.status
	if status == "" {
		if m.refreshing {
			status = "Refreshing..."
		} else {
			status = fmt.Sprintf("Rows: %d  Query: %s  Startup: %s", len(m.records), m.lastQueryDur.Round(time.Millisecond), m.startupDur.Round(time.Millisecond))
		}
	}
	statusStyle := lipgloss.NewStyle()
	if m.statusErr {
		statusStyle = statusStyle.Foreground(lipgloss.Color("9"))
	} else {
		statusStyle = statusStyle.Faint(true)
	}

	return strings.Join([]string{
		title,
		metaLine,
		"",
		list,
		"",
		helpLine,
		statusStyle.Render(status),
	}, "\n")
}

func (m model) renderList() string {
	if m.err != nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("Error: " + m.err.Error())
	}
	if len(m.records) == 0 {
		return lipgloss.NewStyle().Faint(true).Render("No recent files matched your query.")
	}

	visible := m.listVisibleRows()
	end := min(len(m.records), m.top+visible)
	lines := make([]string, 0, end-m.top)
	for i := m.top; i < end; i++ {
		rec := m.records[i]
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}
		name := rec.Name
		if name == "" {
			name = filepath.Base(rec.Path)
		}
		if rec.Missing {
			name = "[missing] " + name
		}
		line := fmt.Sprintf("%s%-32s %-12s %s", prefix, truncate(name, 32), humanizeSince(rec.LastOpened), storage.FormatHomePath(rec.Directory))
		if i == m.cursor {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m model) listVisibleRows() int {
	if m.height <= 0 {
		return 10
	}
	rows := m.height - 8
	if rows < 5 {
		rows = 5
	}
	return rows
}

func (m *model) ensureCursorVisible() {
	if m.cursor < m.top {
		m.top = m.cursor
	}
	visible := m.listVisibleRows()
	if m.cursor >= m.top+visible {
		m.top = m.cursor - visible + 1
	}
	if m.top < 0 {
		m.top = 0
	}
}

func (m model) currentRecord() (storage.FileRecord, bool) {
	if len(m.records) == 0 || m.cursor < 0 || m.cursor >= len(m.records) {
		return storage.FileRecord{}, false
	}
	return m.records[m.cursor], true
}

func (m *model) setStatus(msg string) {
	m.status = msg
	m.statusErr = false
}

func (m *model) setStatusErr(err error) {
	m.status = err.Error()
	m.statusErr = true
}

func (m model) refreshCmd(id int) tea.Cmd {
	search := m.search
	extensions := parseFilter(m.filter)
	limit := m.queryLimit
	store := m.store

	return func() tea.Msg {
		started := time.Now()
		records, err := store.ListRecent(context.Background(), storage.QueryOptions{
			Search:     search,
			Extensions: extensions,
			Limit:      limit,
		})
		return queryResultMsg{records: records, err: err, dur: time.Since(started), id: id}
	}
}

func openExistingPathCmd(path string, requireDir bool) tea.Cmd {
	return func() tea.Msg {
		info, err := os.Stat(path)
		if err != nil {
			return actionResultMsg{err: fmt.Errorf("path unavailable %q: %w", path, err)}
		}
		if requireDir && !info.IsDir() {
			return actionResultMsg{err: fmt.Errorf("not a directory: %q", path)}
		}
		cmd := exec.Command("xdg-open", path)
		if err := cmd.Start(); err != nil {
			return actionResultMsg{err: fmt.Errorf("open %q: %w", path, err)}
		}
		return actionResultMsg{}
	}
}

func checkMissingCmd(records []storage.FileRecord) tea.Cmd {
	recordsCopy := append([]storage.FileRecord(nil), records...)
	return func() tea.Msg {
		byID := make(map[int64]bool, len(recordsCopy))
		for _, rec := range recordsCopy {
			if _, err := os.Stat(rec.Path); err != nil {
				byID[rec.ID] = true
			}
		}
		return missingCheckMsg{byID: byID}
	}
}

func clearStatusAfterDelay() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return clearStatusMsg{}
	})
}

func heartbeatCmd() tea.Cmd {
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
		return heartbeatMsg(t)
	})
}

func parseFilter(raw string) []string {
	filter := strings.TrimSpace(strings.ToLower(raw))
	if filter == "" {
		return nil
	}

	categories := map[string][]string{
		"video":     {"mkv", "mp4", "avi", "mov", "webm"},
		"audio":     {"mp3", "flac", "wav", "ogg"},
		"document":  {"pdf", "epub", "docx", "txt", "md"},
		"documents": {"pdf", "epub", "docx", "txt", "md"},
		"image":     {"png", "jpg", "jpeg", "gif", "webp"},
		"images":    {"png", "jpg", "jpeg", "gif", "webp"},
		"code":      {"go", "ts", "js", "json", "rs", "py", "java", "cpp"},
	}
	if exts, ok := categories[filter]; ok {
		return exts
	}

	parts := strings.Split(filter, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.TrimPrefix(part, "."))
		if part == "" {
			continue
		}
		result = append(result, part)
	}
	return result
}

func humanizeSince(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	if d < 48*time.Hour {
		return "yesterday"
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func trimLastRune(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	return string(r[:len(r)-1])
}

func truncate(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:width])
	}
	if width <= 3 {
		return string(r[:width])
	}
	return string(r[:width-3]) + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, storage.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "recents: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	p := tea.NewProgram(newModel(store), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "recents: %v\n", err)
		os.Exit(1)
	}
}
