package main

import "time"

// openGate holds tracked open events per PID for a short window before
// releasing them, so that mass-open bursts (indexers, thumbnailers, app
// startup scans) are detected and dropped in full instead of leaking their
// first few events into the store.
type openGate struct {
	// holdWindow is how long an event is held before release. It doubles as
	// the burst-detection window: a burst arriving within holdWindow of an
	// event cancels that event before it is ever released.
	holdWindow time.Duration
	// maxOpens is the number of distinct paths a PID may open within
	// holdWindow before it is considered bursting and suppressed.
	maxOpens int
	// cooldown is the sliding suppression period once a PID bursts; any
	// further event while suppressed extends it.
	cooldown time.Duration
	// idleTTL is how long an inactive PID's state is kept before cleanup.
	idleTTL time.Duration

	pids map[int]*pidGateState
}

type gateEvent struct {
	path string
	pid  int
	at   time.Time
}

type pidGateState struct {
	pending         []gateEvent
	recentPaths     map[string]time.Time
	suppressedUntil time.Time
	lastActivity    time.Time
}

func newOpenGate(holdWindow time.Duration, maxOpens int, cooldown time.Duration) *openGate {
	return &openGate{
		holdWindow: holdWindow,
		maxOpens:   maxOpens,
		cooldown:   cooldown,
		idleTTL:    10 * holdWindow,
		pids:       make(map[int]*pidGateState, 64),
	}
}

// Add records an open event for pid. The event is not released to the caller
// until it has been held for holdWindow without the PID bursting.
func (g *openGate) Add(pid int, path string, at time.Time) {
	st := g.pids[pid]
	if st == nil {
		st = &pidGateState{recentPaths: make(map[string]time.Time, 8)}
		g.pids[pid] = st
	}
	st.lastActivity = at

	if at.Before(st.suppressedUntil) {
		// A sustained scan keeps the PID suppressed for as long as it runs.
		st.suppressedUntil = at.Add(g.cooldown)
		st.pending = st.pending[:0]
		return
	}

	cutoff := at.Add(-g.holdWindow)
	for p, ts := range st.recentPaths {
		if ts.Before(cutoff) {
			delete(st.recentPaths, p)
		}
	}

	// Repeat opens of the same path within the window (players commonly open
	// a file more than once) neither count toward the burst budget nor create
	// duplicate events; the ingest debounce would drop them anyway.
	if _, seen := st.recentPaths[path]; seen {
		return
	}
	st.recentPaths[path] = at
	st.pending = append(st.pending, gateEvent{path: path, pid: pid, at: at})

	if len(st.recentPaths) > g.maxOpens {
		// Burst: drop everything still held for this PID and suppress it.
		st.pending = st.pending[:0]
		st.suppressedUntil = at.Add(g.cooldown)
	}
}

// Flush releases events that have been held for at least holdWindow and
// cleans up idle PID state.
func (g *openGate) Flush(now time.Time) []gateEvent {
	var out []gateEvent
	for pid, st := range g.pids {
		if len(st.pending) > 0 {
			kept := st.pending[:0]
			for _, ev := range st.pending {
				if now.Sub(ev.at) >= g.holdWindow {
					out = append(out, ev)
				} else {
					kept = append(kept, ev)
				}
			}
			st.pending = kept
		}
		if len(st.pending) == 0 && now.Sub(st.lastActivity) > g.idleTTL && now.After(st.suppressedUntil) {
			delete(g.pids, pid)
		}
	}
	return out
}

// FlushAll releases every held event regardless of age. Used on shutdown so
// the user's last opens are not lost.
func (g *openGate) FlushAll() []gateEvent {
	var out []gateEvent
	for _, st := range g.pids {
		out = append(out, st.pending...)
		st.pending = st.pending[:0]
	}
	return out
}
