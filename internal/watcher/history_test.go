package watcher

import (
	"context"
	"testing"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// A long history of closed cursors must not hide the open one: the tick finds
// the open cursor even when it is older than the closed rows. Without this the
// tick would reread the board as new and rewrite an unchanged cursor.
func TestOpenCursorSurvivesLongClosedHistory(t *testing.T) {
	b := &fakeBoard{tasks: []board.Task{tickTask("t-1", "p", "dev", "open")}}
	s, _ := newStorePath(t)
	c := newClock("2026-11-08T12:00:00Z")
	w := testWatcher(b, s, c)
	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	open := openCursorID(t, s)

	// 300 closed rows: more than the old 256-row window, far less than the
	// current one, so the test stays fast and still fails on the old bound.
	for i := 0; i < 300; i++ {
		text, err := state.Encode(state.Cursor{})
		if err != nil {
			t.Fatal(err)
		}
		id, err := s.Put(state.KindCursor, state.CursorScope, "history", text)
		if err != nil {
			t.Fatalf("put history %d: %v", i, err)
		}
		if err := s.Close(id); err != nil {
			t.Fatalf("close history %d: %v", i, err)
		}
	}

	before := countOpen(t, s, state.CursorScope, state.KindCursor)
	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatalf("unchanged tick: %v", err)
	}
	if got := openCursorID(t, s); got != open {
		t.Fatalf("open cursor %d was replaced by %d after an unchanged tick", open, got)
	}
	if after := countOpen(t, s, state.CursorScope, state.KindCursor); after != before {
		t.Fatalf("open cursors %d -> %d on an unchanged board", before, after)
	}
}
