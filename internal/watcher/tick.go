package watcher

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// Tick is the whole clock path: one board read, a comparison, the report, and
// at most one cursor write. It never launches anything; Watcher has no launcher.
func (w *Watcher) Tick(ctx context.Context) (Report, error) {
	now := w.now()
	report := Report{At: now}

	tasks, err := w.Board.Status(ctx)
	if err != nil {
		return report, fmt.Errorf("read board: %w", err)
	}

	cursorRows, err := w.Store.Recent(state.CursorScope, cursorLimit)
	if err != nil {
		return report, fmt.Errorf("read cap.cursor: %w", err)
	}
	old, err := state.CursorFrom(cursorRows)
	if err != nil {
		return report, fmt.Errorf("read cap.cursor: %w", err)
	}

	rows, err := listRows(w.Store, tasks, old)
	if err != nil {
		return report, fmt.Errorf("read cap state: %w", err)
	}
	open, err := state.OpenActs(rows)
	if err != nil {
		return report, err
	}

	byTask := make(map[string]board.Task, len(tasks))
	for _, t := range tasks {
		byTask[t.Task] = t
	}
	for _, a := range open {
		if a.Observed == state.ObservedConfirmed {
			continue
		}
		t, seen := byTask[a.Task]
		if seen && t.State == "claimed" {
			updated := a
			updated.Observed = state.ObservedConfirmed
			updated.ConfirmedAt = &now
			updated.Session = t.Session
			if err := w.rewriteAct(a, updated); err != nil {
				return report, err
			}
			report.Confirmed = append(report.Confirmed, updated)
			continue
		}
		report.Pending = append(report.Pending, a)
	}

	next := snapshot(tasks)
	report.Changes = diff(old, next)
	report.Changed = (old != nil && len(report.Changes) > 0) || (old == nil && len(next) > 0)

	if report.Changed {
		recs, err := w.recommend(tasks, rows, now)
		if err != nil {
			return report, err
		}
		report.Recommendations = recs
		// The cursor is last: a failed knowledge read must leave the board
		// looking changed, so the next tick retries the recommendation.
		if err := w.writeCursor(cursorRows, next, now); err != nil {
			return report, err
		}
	}

	w.trace(map[string]any{
		"kind":            "tick",
		"at":              now.UTC().Format(time.RFC3339Nano),
		"changed":         report.Changed,
		"changes":         len(report.Changes),
		"confirmed":       len(report.Confirmed),
		"pending":         len(report.Pending),
		"recommendations": len(report.Recommendations),
	})
	return report, nil
}

func (w *Watcher) rewriteAct(old, updated state.Act) error {
	if err := w.Store.Close(old.ID); err != nil {
		return fmt.Errorf("close cap.act %d: %w", old.ID, err)
	}
	text, err := state.Encode(updated)
	if err != nil {
		return err
	}
	if _, err := w.Store.Put(state.KindAct, state.Scope(updated.Project), actTitle(updated), text); err != nil {
		return fmt.Errorf("write cap.act: %w", err)
	}
	return nil
}

func (w *Watcher) writeCursor(existing []state.Row, tasks []state.CursorTask, at time.Time) error {
	for _, r := range existing {
		if r.Kind == state.KindCursor && !r.Closed {
			if err := w.Store.Close(r.ID); err != nil {
				return fmt.Errorf("close cap.cursor %d: %w", r.ID, err)
			}
		}
	}
	text, err := state.Encode(state.Cursor{At: at, Tasks: tasks})
	if err != nil {
		return err
	}
	if _, err := w.Store.Put(state.KindCursor, state.CursorScope, "board cursor", text); err != nil {
		return fmt.Errorf("write cap.cursor: %w", err)
	}
	return nil
}

// snapshot keeps every task that is not done: open and claimed both matter,
// because a lapsed lease reads as open.
func snapshot(tasks []board.Task) []state.CursorTask {
	out := make([]state.CursorTask, 0, len(tasks))
	for _, t := range tasks {
		if t.State == "done" {
			continue
		}
		out = append(out, state.CursorTask{
			Task:         t.Task,
			Project:      t.Project,
			Kind:         t.Kind,
			State:        t.State,
			Session:      t.Session,
			LeaseExpires: t.LeaseExpires,
			LeaseLive:    t.LeaseLive,
			NewestReport: newestReport(t),
		})
	}
	return out
}

func newestReport(t board.Task) *time.Time {
	var newest *time.Time
	take := func(at *time.Time) {
		if at == nil {
			return
		}
		if newest == nil || at.After(*newest) {
			v := at.UTC()
			newest = &v
		}
	}
	for _, e := range t.Evidence {
		take(e.At)
	}
	take(t.Agent.At)
	take(t.Runtime.At)
	return newest
}

func diff(old *state.Cursor, next []state.CursorTask) []Change {
	oldTasks := map[string]state.CursorTask{}
	if old != nil {
		for _, t := range old.Tasks {
			oldTasks[t.Task] = t
		}
	}
	nextTasks := map[string]state.CursorTask{}
	for _, t := range next {
		nextTasks[t.Task] = t
	}

	var changes []Change
	for _, n := range next {
		o, ok := oldTasks[n.Task]
		if !ok {
			changes = append(changes, Change{Task: n.Task, Project: n.Project, Reason: "new " + n.Kind + " task, " + holder(n)})
			continue
		}
		var why []string
		if o.State != n.State || o.Session != n.Session {
			why = append(why, "holder "+holder(o)+" -> "+holder(n))
		}
		if o.LeaseLive != n.LeaseLive || !sameTime(o.LeaseExpires, n.LeaseExpires) {
			why = append(why, "lease "+lease(o)+" -> "+lease(n))
		}
		if !sameTime(o.NewestReport, n.NewestReport) {
			why = append(why, "newest report "+instant(o.NewestReport)+" -> "+instant(n.NewestReport))
		}
		if len(why) > 0 {
			changes = append(changes, Change{Task: n.Task, Project: n.Project, Reason: strings.Join(why, "; ")})
		}
	}
	if old != nil {
		for _, o := range old.Tasks {
			if _, ok := nextTasks[o.Task]; !ok {
				changes = append(changes, Change{Task: o.Task, Project: o.Project, Reason: "no longer open"})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Project == changes[j].Project {
			return changes[i].Task < changes[j].Task
		}
		return changes[i].Project < changes[j].Project
	})
	return changes
}

func holder(t state.CursorTask) string {
	if t.Session == "" {
		return "unheld"
	}
	if t.LeaseLive {
		return t.Session + " (live)"
	}
	return t.Session + " (lapsed)"
}

func lease(t state.CursorTask) string {
	if t.LeaseExpires == nil {
		return "-"
	}
	if !t.LeaseLive {
		return "lapsed"
	}
	return "live until " + instant(t.LeaseExpires)
}

func instant(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
