package watcher

import (
	"strings"
	"testing"
	"time"

	"github.com/IanLuo/cap/internal/board"
)

// The gate reads the newest act's launched_at, closed rows included. A tick
// that confirms an act closes it and writes a new row; this test shows the gate
// still refuses a second instruction inside 90 s after that rewrite.
func TestGateHoldsAcrossConfirmRewrite(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{{Task: "t-1", Project: "p", Kind: "dev", State: "open", ProjectPath: "/tmp/p"}}}
	s, path := newStorePath(t)
	c := newClock("2026-11-08T12:00:00Z")
	seedKnowledge(t, path, "cap.dispatch: echo {task}\ncap.agent: fake")
	l := &fakeLauncher{}
	d := testDispatcher(b, s, l, c)
	w := testWatcher(b, s, c)

	if _, err := d.Act(ctx(), "t-1"); err != nil {
		t.Fatalf("act: %v", err)
	}

	b.tasks[0].State = "claimed"
	b.tasks[0].Session = "sess-1"
	b.tasks[0].LeaseLive = true
	c.Advance(5 * time.Second)
	if _, err := w.Tick(ctx()); err != nil {
		t.Fatalf("confirming tick: %v", err)
	}
	if a := openAct(t, s, "p", "t-1"); a.Observed != "confirmed" {
		t.Fatalf("act not confirmed before the gate check: %+v", a)
	}

	// The holder is gone and the lease lapsed; the board reads open again.
	b.tasks[0].State = "open"
	b.tasks[0].Session = ""
	b.tasks[0].LeaseLive = false
	c.Advance(20 * time.Second)
	_, err := d.Act(ctx(), "t-1")
	if err == nil || !IsRefusal(err) {
		t.Fatalf("instruction 25 s after launch = %v, want gate refusal", err)
	}
	if !strings.Contains(err.Error(), "2026-11-08T12:00:00Z") {
		t.Fatalf("refusal does not name the launch: %v", err)
	}
	if len(l.calls) != 1 {
		t.Fatalf("launches = %d, want 1", len(l.calls))
	}
}
