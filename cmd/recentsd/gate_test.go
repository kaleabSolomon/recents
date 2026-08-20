package main

import (
	"testing"
	"time"
)

func TestOpenGateReleasesSingleOpenAfterHold(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	g := newOpenGate(2*time.Second, 3, 5*time.Second)

	g.Add(100, "/home/u/a.pdf", base)

	if got := g.Flush(base.Add(time.Second)); len(got) != 0 {
		t.Fatalf("event released before hold window elapsed: %v", got)
	}
	got := g.Flush(base.Add(2 * time.Second))
	if len(got) != 1 || got[0].path != "/home/u/a.pdf" {
		t.Fatalf("Flush = %v, want single a.pdf event", got)
	}
	if again := g.Flush(base.Add(3 * time.Second)); len(again) != 0 {
		t.Fatalf("event released twice: %v", again)
	}
}

func TestOpenGateDropsEntireBurst(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	g := newOpenGate(2*time.Second, 3, 5*time.Second)

	paths := []string{"/h/a.jpg", "/h/b.jpg", "/h/c.jpg", "/h/d.jpg", "/h/e.jpg"}
	for i, p := range paths {
		g.Add(200, p, base.Add(time.Duration(i)*100*time.Millisecond))
	}

	if got := g.Flush(base.Add(10 * time.Second)); len(got) != 0 {
		t.Fatalf("burst events leaked: %v", got)
	}

	// While the scan continues, suppression slides forward.
	g.Add(200, "/h/f.jpg", base.Add(4*time.Second))
	if got := g.Flush(base.Add(20 * time.Second)); len(got) != 0 {
		t.Fatalf("event during suppression leaked: %v", got)
	}

	// After the PID has been quiet past the cooldown, opens flow again.
	late := base.Add(30 * time.Second)
	g.Add(200, "/h/g.jpg", late)
	if got := g.Flush(late.Add(2 * time.Second)); len(got) != 1 {
		t.Fatalf("post-cooldown open not released: %v", got)
	}
}

func TestOpenGateDedupesRepeatOpensOfSamePath(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	g := newOpenGate(2*time.Second, 3, 5*time.Second)

	// A player probing then playing the same file several times must count as
	// one open and must not trip the burst budget.
	for i := 0; i < 6; i++ {
		g.Add(300, "/h/movie.mkv", base.Add(time.Duration(i)*200*time.Millisecond))
	}
	g.Add(300, "/h/other.mkv", base.Add(1500*time.Millisecond))

	got := g.Flush(base.Add(5 * time.Second))
	if len(got) != 2 {
		t.Fatalf("Flush returned %d events, want 2 (deduped): %v", len(got), got)
	}
}

func TestOpenGateCleansUpIdlePIDs(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	g := newOpenGate(2*time.Second, 3, 5*time.Second)

	g.Add(400, "/h/a.txt", base)
	g.Flush(base.Add(2 * time.Second))
	g.Flush(base.Add(60 * time.Second))

	if len(g.pids) != 0 {
		t.Fatalf("idle PID state not cleaned up: %d entries", len(g.pids))
	}
}

func TestOpenGateFlushAllReleasesHeldEvents(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	g := newOpenGate(2*time.Second, 3, 5*time.Second)

	g.Add(500, "/h/a.txt", base)
	if got := g.FlushAll(); len(got) != 1 {
		t.Fatalf("FlushAll = %v, want the held event", got)
	}
}
