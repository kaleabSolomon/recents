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

	"recents/internal/storage"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	grouped      bool
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
		store:       store,
		queryLimit:  500,
		refreshing:  true,
		startedAt:   time.Now(),
		lastQueryID: 1,
	}
}

func (m model) Init() tea.Cmd {
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
	case "d":
		m.grouped = !m.grouped
		m.cursor = 0
		m.top = 0
		m.refreshing = true
		m.lastQueryID++
		return m, m.refreshCmd(m.lastQueryID)
	case "esc":
		if m.filter != "" || m.mode != modeNormal {
			m.filter = ""
			m.filterInput = ""
			m.mode = modeNormal
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

var (
	textStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("247"))                                            // Gray
	accentStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Bold(true)                                  // Bright Cyan
	subtleStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("239"))                                            // Darker Gray
	highlightStyle   = lipgloss.NewStyle().Background(lipgloss.Color("23")).Foreground(lipgloss.Color("51")).Bold(true) // Dark Cyan BG, Bright Cyan FG
	titleStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("43")).Bold(true).Margin(0, 1)                     // Light Cyan
	boxStyle         = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("24"))      // Dark Teal
	activeBoxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("37"))      // Medium Cyan
	dirStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("38"))                                             // Deep Cyan
	timeStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("73"))                                             // Muted Cyan
	tableHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("37")).Bold(true).BorderBottom(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("24"))
	errorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("160")).Bold(true) // Darker Red
)

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Initializing..."
	}

	searchVal := m.search
	if m.mode == modeSearch {
		searchVal = m.searchInput + "█"
	}
	if searchVal == "" {
		searchVal = "..."
	}

	filterVal := m.filter
	if m.mode == modeFilter {
		filterVal = m.filterInput + "█"
	}
	if filterVal == "" {
		filterVal = "..."
	}

	// === HEADER ===
	title := titleStyle.Render("⚡ recents")

	searchLabel := textStyle.Render("Search ")
	searchContent := accentStyle.Render(searchVal)
	if m.mode == modeSearch {
		searchContent = highlightStyle.Render(" " + searchVal + " ")
	}
	searchBlock := lipgloss.JoinHorizontal(lipgloss.Center, searchLabel, searchContent)

	filterLabel := textStyle.Render("Filter ")
	filterContent := accentStyle.Render(filterVal)
	if m.mode == modeFilter {
		filterContent = highlightStyle.Render(" " + filterVal + " ")
	}
	filterBlock := lipgloss.JoinHorizontal(lipgloss.Center, filterLabel, filterContent)

	viewVal := "files"
	if m.grouped {
		viewVal = "folders"
	}
	viewBlock := lipgloss.JoinHorizontal(lipgloss.Center, textStyle.Render("View "), accentStyle.Render(viewVal))

	headerItems := lipgloss.JoinHorizontal(lipgloss.Center, title, subtleStyle.Render(" | "), searchBlock, subtleStyle.Render(" | "), filterBlock, subtleStyle.Render(" | "), viewBlock)

	headerBox := boxStyle.Width(m.width - 2).Render(headerItems)
	if m.mode == modeSearch || m.mode == modeFilter {
		headerBox = activeBoxStyle.Width(m.width - 2).Render(headerItems)
	}

	// === FOOTER ===
	helpText := ""
	keys := []string{"j/k,↑/↓", "move", "g/G", "top/bot", "Enter", "open", "Ctrl+o", "folder", "/", "search", "f", "filter", "d", "folders", "Esc", "clear", "r", "refresh", "q", "quit"}
	for i := 0; i < len(keys); i += 2 {
		helpText += accentStyle.Render(keys[i]) + subtleStyle.Render(" "+keys[i+1]+"  ")
	}

	status := m.status
	if status == "" {
		if m.refreshing {
			status = "Refreshing..."
		} else {
			status = fmt.Sprintf("Rows: %d  Query: %s  Startup: %s", len(m.records), m.lastQueryDur.Round(time.Millisecond), m.startupDur.Round(time.Millisecond))
		}
	}
	renderedStatus := subtleStyle.Render(status)
	if m.statusErr {
		renderedStatus = errorStyle.Render(status)
	}

	footerInner := lipgloss.JoinHorizontal(lipgloss.Bottom, helpText, strings.Repeat(" ", max(0, m.width-4-lipgloss.Width(helpText)-lipgloss.Width(renderedStatus))), renderedStatus)
	footerBox := boxStyle.Width(m.width - 2).Render(footerInner)

	// === LIST BODY ===
	listHeight := m.height - lipgloss.Height(headerBox) - lipgloss.Height(footerBox) - 2 // -2 for borders
	if listHeight < 0 {
		listHeight = 0
	}

	var listContent string
	if m.err != nil {
		listContent = errorStyle.Render("Error: " + m.err.Error())
	} else if len(m.records) == 0 {
		listContent = subtleStyle.Render("\n  No recent files matched your query.")
	} else {
		// Calculate column widths
		avSpace := m.width - 4 // border + padding
		ageWidth := 11
		dirWidth := avSpace / 3
		if dirWidth > 40 {
			dirWidth = 40
		}
		if dirWidth < 15 {
			dirWidth = 15
		}
		fileWidth := avSpace - ageWidth - dirWidth - 6 // margins
		if fileWidth < 10 {
			fileWidth = 10
		}

		// Table Header
		thLine := fmt.Sprintf("  %-*s   %-*s   %s", fileWidth, "FILE", ageWidth, "AGE", "DIRECTORY")
		listContent = tableHeaderStyle.Render(thLine) + "\n"

		// Rows
		visible := m.listVisibleRows()
		end := min(len(m.records), m.top+visible)
		lines := make([]string, 0, end-m.top)

		for i := m.top; i < end; i++ {
			rec := m.records[i]

			cursor := "  "
			if i == m.cursor {
				cursor = accentStyle.Render("▶ ")
			}

			name := rec.Name
			if name == "" {
				name = filepath.Base(rec.Path)
			}
			if rec.Missing {
				name = "❌ " + name
			}

			dispName := truncate(name, fileWidth)
			dispAge := humanizeSince(rec.LastOpened)
			dir := storage.FormatHomePath(rec.Directory)
			dispDir := truncate(dir, dirWidth)
			if m.grouped && rec.GroupCount > 1 {
				// Keep the file count visible even when the path is truncated.
				suffix := fmt.Sprintf(" (%d)", rec.GroupCount)
				dispDir = truncate(dir, max(1, dirWidth-len(suffix))) + suffix
			}

			rowStyle := textStyle
			rTimeStyle := timeStyle
			rDirStyle := dirStyle

			if i == m.cursor {
				rowStyle = highlightStyle
				rTimeStyle = highlightStyle
				rDirStyle = highlightStyle
			} else if rec.Missing {
				rowStyle = subtleStyle
				rTimeStyle = subtleStyle
				rDirStyle = subtleStyle
			}

			row := fmt.Sprintf("%s%s   %s   %s",
				cursor,
				rowStyle.Render(fmt.Sprintf("%-*s", fileWidth, dispName)),
				rTimeStyle.Render(fmt.Sprintf("%-*s", ageWidth, dispAge)),
				rDirStyle.Render(fmt.Sprintf("%-*s", dirWidth, dispDir)),
			)
			lines = append(lines, row)
		}

		padRows := visible - len(lines)
		for j := 0; j < padRows; j++ {
			lines = append(lines, "")
		}

		listContent += strings.Join(lines, "\n")
	}

	mainBoxStyle := boxStyle
	if m.mode == modeNormal {
		mainBoxStyle = activeBoxStyle
	}

	mainBox := mainBoxStyle.Width(m.width - 2).Height(listHeight).Render(listContent)

	return lipgloss.JoinVertical(lipgloss.Left, headerBox, mainBox, footerBox)
}

func (m model) listVisibleRows() int {
	// m.height total = header(3) + footer(3) + mainBox(listHeight+2 borders).
	// List internal space = listHeight
	// Top header line in list = 1
	// Visible rows = listHeight - 1
	listHeight := m.height - 8
	if listHeight < 5 {
		listHeight = 5
	}
	return listHeight - 1 // subtract table header
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
	grouped := m.grouped
	store := m.store

	return func() tea.Msg {
		started := time.Now()
		records, err := store.ListRecent(context.Background(), storage.QueryOptions{
			Search:     search,
			Extensions: extensions,
			Limit:      limit,
			GroupByDir: grouped,
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
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
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
