package watcher

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/IanLuo/cap/internal/agents"
	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// Tier-1 action classes. An action triggers only on a transition between the
// previous cursor and this read, never on elapsed time alone.
const (
	ClassReap  = "reap"
	ClassClose = "close"
	ClassNudge = "nudge"

	// DefaultCloseGrace is how long an empty pane with no launch row must be
	// seen before cap closes it. A transition starts the timer; the grace only
	// delays an action already triggered.
	DefaultCloseGrace = 300 * time.Second

	// maxNudgesPerRound bounds prompts to one stalled agent in one fs round.
	maxNudgesPerRound = 2
)

func (w *Watcher) closeGrace() time.Duration {
	if w.CloseGrace == 0 {
		return DefaultCloseGrace
	}
	return w.CloseGrace
}

// tier1 reads the transitions between the previous cursor and this tick and
// takes at most one action per task and class inside the gate. It never
// dispatches, never closes an fs-launched pane, and never answers a blocked
// agent. A side effect that fails is recorded and reported, not retried.
// paneBaseline says the previous cursor holds no pane snapshot, so no pane can
// have moved; the reap keys off the board change instead, and still runs.
func (w *Watcher) tier1(ctx context.Context, old *state.Cursor, tasks []board.Task, panes []agents.Pane, rows []state.Row, changes []Change, paneBaseline bool) ([]state.Action, []string, error) {
	actions, err := state.Actions(rows)
	if err != nil {
		return nil, nil, err
	}
	e := &engine{w: w, ctx: ctx, now: w.now(), old: old, tasks: tasks, panes: panes, rows: rows, actions: actions, spoken: map[string]bool{}}

	paneByID := make(map[string]agents.Pane, len(panes))
	for _, p := range panes {
		paneByID[p.ID] = p
	}
	launchByPane := map[string]board.Task{}
	for _, t := range tasks {
		if t.Process.Pane != "" {
			launchByPane[t.Process.Pane] = t
		}
	}
	oldPanes := map[string]state.CursorPane{}
	for _, p := range old.Panes {
		oldPanes[p.Pane] = p
	}

	var ids []string
	if !paneBaseline {
		ids = make([]string, 0, len(panes))
		for id := range paneByID {
			ids = append(ids, id)
		}
		sort.Strings(ids)
	}
	for _, id := range ids {
		p := paneByID[id]
		prev, seen := oldPanes[id]
		transition := !seen || prev.Status != p.Status || prev.Agent != p.Agent

		if p.Agent == "" {
			if _, fsOwned := launchByPane[id]; fsOwned {
				continue // fs closes its own panes; class 4 owns that
			}
			if !seen || transition {
				continue // the grace starts now, or restarts on a new class
			}
			if !prev.WasAgent {
				continue // never ran an agent: a human terminal, not an orphan
			}
			if e.now.Sub(prev.First) >= w.closeGrace() {
				e.closeEmpty(p, prev.First)
			}
			continue
		}

		t, launched := launchByPane[id]
		if !launched {
			// An agent pane no launch row owns is human-owned. cap only
			// announces a blocked one; it never closes it.
			if p.Status == "blocked" && transition {
				e.blockedOrphan(p)
			}
			continue
		}
		switch p.Status {
		case "blocked":
			if transition {
				e.blocked(t, p)
			}
		case "idle":
			// fs reads a lapsed claim as an open task that still names its
			// session, so a holder plus a lapsed lease is the nudge case. A
			// done task's pane is fs's to reap, never a prompt.
			if t.State == "done" || t.Session == "" || t.LeaseLive {
				continue
			}
			leaseLapsed := false
			for _, o := range old.Tasks {
				if o.Task == t.Task && o.LeaseLive {
					leaseLapsed = true
				}
			}
			if transition || leaseLapsed {
				e.nudge(t, p)
			}
		}
	}

	e.reapClosed(changes, paneByID)
	if e.err != nil {
		return e.taken, e.refused, e.err
	}
	return e.taken, e.refused, nil
}

type engine struct {
	w       *Watcher
	ctx     context.Context
	now     time.Time
	old     *state.Cursor
	tasks   []board.Task
	panes   []agents.Pane
	rows    []state.Row
	actions []state.Action
	taken   []state.Action
	refused []string
	err     error
	// orphanAsked is this tick's own answer to hasQuestion for the one
	// board-wide gap: rows were read at tick start and cannot see a question
	// written moments earlier in the same tick.
	orphanAsked bool
	// spoken holds every task cap nudged or announced this tick. The reap
	// skips their panes: fs closes an idle agent on a lapsed lease, and cap
	// does not close a pane it just asked the human or the agent to answer.
	spoken map[string]bool
}

// reapClosed calls fs reap once per project whose round just ended while the
// pane fs launched is still open. fs decides which pane to close; cap notices
// the transition that ended the round: the task left the board, its lease
// lapsed, or its session's newest review word became fail or blocked.
func (e *engine) reapClosed(changes []Change, paneByID map[string]agents.Pane) {
	if e.w.Reap == nil || e.old == nil {
		return
	}
	oldByTask := map[string]state.CursorTask{}
	for _, t := range e.old.Tasks {
		oldByTask[t.Task] = t
	}

	type roundEnd struct{ project, task, pane, why string }
	var ended []roundEnd
	reaped := map[string]bool{}
	add := func(r roundEnd) {
		if reaped[r.project] {
			return
		}
		reaped[r.project] = true
		ended = append(ended, r)
	}

	// A task that left the board carries its pane from the previous cursor.
	for _, c := range changes {
		if !strings.HasPrefix(c.Reason, "no longer open") {
			continue
		}
		o, ok := oldByTask[c.Task]
		if !ok || o.Pane == "" {
			continue
		}
		if _, open := paneByID[o.Pane]; !open {
			continue
		}
		add(roundEnd{c.Project, c.Task, o.Pane, "round closed for task " + c.Task + ", pane " + o.Pane + " still open"})
	}

	// A task still on the board whose round ended without the task closing.
	for _, t := range e.tasks {
		if t.Process.Pane == "" || reaped[t.Project] {
			continue
		}
		if _, open := paneByID[t.Process.Pane]; !open {
			continue
		}
		o, known := oldByTask[t.Task]
		if !known {
			continue
		}
		switch {
		case o.LeaseLive && !t.LeaseLive && !e.spoken[t.Task]:
			add(roundEnd{t.Project, t.Task, t.Process.Pane, "lease lapsed on task " + t.Task + ", pane " + t.Process.Pane + " still open"})
		case reviewVerdictEnded(o, t):
			add(roundEnd{t.Project, t.Task, t.Process.Pane, "review verdict " + t.Agent.Reported + " on task " + t.Task + ", pane " + t.Process.Pane + " still open"})
		}
	}

	for _, r := range ended {
		project := r.project
		e.perform(state.Action{
			Class:   ClassReap,
			Target:  project,
			Project: project,
			Trigger: r.why,
		}, func() error { return e.w.Reap.Reap(e.ctx, project) })
	}
}

// reviewVerdictEnded reports whether the task's session has just said fail or
// blocked as a reviewer. Only the newest word counts: a verdict already in the
// previous cursor ended a round that has been reaped.
func reviewVerdictEnded(o state.CursorTask, t board.Task) bool {
	if t.Agent.Role != "review" || t.Agent.At == nil {
		return false
	}
	if t.Agent.Reported != "fail" && t.Agent.Reported != "blocked" {
		return false
	}
	return o.NewestReport == nil || t.Agent.At.After(*o.NewestReport)
}

func (e *engine) closeEmpty(p agents.Pane, first time.Time) {
	e.perform(state.Action{
		Class:   ClassClose,
		Target:  p.ID,
		Pane:    p.ID,
		Trigger: "orphaned pane, empty since " + first.UTC().Format(time.RFC3339),
	}, func() error { return e.w.Agents.Close(e.ctx, p.ID) })
}

func (e *engine) nudge(t board.Task, p agents.Pane) {
	target := t.Task
	if n := state.CountActionsInRound(append(e.actions, e.taken...), ClassNudge, target, t.Round); n >= maxNudgesPerRound {
		e.refused = append(e.refused, fmt.Sprintf("nudge %s: round %d already nudged %d times", t.Task, t.Round, n))
		return
	}
	text := nudgeText(t)
	// A pane cap just spoke to is not reaped in the same tick: fs closes an
	// idle agent whose lease lapsed, and the prod must get its chance to renew.
	e.spoken[target] = true
	e.perform(state.Action{
		Class:   ClassNudge,
		Target:  target,
		Project: t.Project,
		Task:    t.Task,
		Pane:    p.ID,
		Round:   t.Round,
		Trigger: "lapsed claim, agent idle",
	}, func() error { return e.w.Agents.Prompt(e.ctx, p.ID, text) })
}

func nudgeText(t board.Task) string {
	checks := "none declared"
	if len(t.Checks) > 0 {
		checks = strings.Join(t.Checks, "; ")
	}
	return fmt.Sprintf("You are the worker for fs task %s in project %s. Goal: %s. Declared checks: %s. Your lease on the task has lapsed while the task is still claimed. Report progress now with: fs report --project %s --session %s --task %s --state progress --note \"cap: nudge\" and then continue the work. Do not call fs done.",
		t.Task, t.Project, t.Goal, checks, t.Project, t.Session, t.Task)
}

func (e *engine) blocked(t board.Task, p agents.Pane) {
	// Announcing a blocked agent must not be undone in the same tick: the
	// human answers, and the pane waits for that answer.
	e.spoken[t.Task] = true
	missing := "blocked: agent is waiting on a prompt"
	if !e.hasQuestion(t.Task, missing) {
		if err := e.ask(t.Task, t.Project, state.Scope(t.Project), missing); err != nil {
			e.err = err
			return
		}
	}
	body := fmt.Sprintf("%s %s — %s (pane %s)", t.Project, t.Task, t.Goal, p.ID)
	if err := e.w.Agents.Notify(e.ctx, "cap: agent blocked", body); err != nil {
		e.refused = append(e.refused, "notify "+t.Task+": "+err.Error())
	}
}

func (e *engine) blockedOrphan(p agents.Pane) {
	missing := "blocked: agent is waiting on a prompt"
	if !e.orphanAsked && !e.hasQuestion("", missing) {
		if err := e.ask("", "", state.CursorScope, missing); err != nil {
			e.err = err
			return
		}
		e.orphanAsked = true
	}
	body := fmt.Sprintf("unowned agent blocked in %s (pane %s)", p.CWD, p.ID)
	if err := e.w.Agents.Notify(e.ctx, "cap: agent blocked", body); err != nil {
		e.refused = append(e.refused, "notify "+p.ID+": "+err.Error())
	}
}

func (e *engine) hasQuestion(task, missing string) bool {
	qs, err := state.Questions(e.rows, task)
	if err != nil {
		e.err = err
		return true
	}
	for _, q := range qs {
		if q.Missing == missing {
			return true
		}
	}
	return false
}

func (e *engine) ask(task, project, scope, missing string) error {
	text, err := state.Encode(state.Question{Task: task, Project: project, Missing: missing, At: e.now})
	if err != nil {
		return err
	}
	if _, err := e.w.Store.Put(state.KindQuestion, scope, "blocked agent", text); err != nil {
		return fmt.Errorf("write cap.question: %w", err)
	}
	return nil
}

// perform records the action before the side effect, runs it, then closes the
// attempt with its outcome. The row for an attempt is never rewritten.
func (e *engine) perform(a state.Action, run func() error) {
	if g := e.gated(a.Class, a.Target); g != nil {
		e.refused = append(e.refused, fmt.Sprintf("%s %s: earlier action at %s", a.Class, a.Target, g.At.UTC().Format(time.RFC3339)))
		return
	}
	a.At = e.now
	a.Outcome = state.ActionAttempted
	id, err := e.put(a)
	if err != nil {
		e.err = err
		return
	}
	a.ID = id
	if run != nil {
		if err := run(); err != nil {
			e.finish(a, state.ActionFailed+": "+err.Error())
			e.refused = append(e.refused, a.Class+" "+a.Target+" failed: "+err.Error())
			return
		}
	}
	e.finish(a, state.ActionDone)
}

func (e *engine) gated(class, target string) *state.Action {
	all := append(append([]state.Action{}, e.actions...), e.taken...)
	last := state.LatestAction(all, class, target)
	if last == nil || e.now.Sub(last.At) >= e.w.gate() {
		return nil
	}
	return last
}

func (e *engine) put(a state.Action) (int64, error) {
	text, err := state.Encode(a)
	if err != nil {
		return 0, err
	}
	id, err := e.w.Store.Put(state.KindAction, actionScope(a), actionTitle(a), text)
	if err != nil {
		return 0, fmt.Errorf("write cap.action: %w", err)
	}
	return id, nil
}

// finish closes the attempted row and writes the outcome as a new row.
func (e *engine) finish(a state.Action, outcome string) {
	if err := e.w.Store.Close(a.ID); err != nil {
		e.err = fmt.Errorf("close cap.action %d: %w", a.ID, err)
		return
	}
	done := a
	done.ID = 0
	done.Outcome = outcome
	id, err := e.put(done)
	if err != nil {
		e.err = err
		return
	}
	done.ID = id
	e.taken = append(e.taken, done)
}

func actionScope(a state.Action) string {
	if a.Project == "" {
		return state.CursorScope
	}
	return state.Scope(a.Project)
}

func actionTitle(a state.Action) string {
	return a.Class + " " + a.Target
}

// paneSnapshot carries the harness read into the cursor. First preserves the
// first sighting of a pane in the same class, so the close grace survives
// replays; WasAgent remembers that the pane once ran an agent, which is what
// makes an empty pane an orphan rather than a terminal.
func paneSnapshot(panes []agents.Pane, old *state.Cursor, now time.Time) []state.CursorPane {
	oldFirst := map[string]state.CursorPane{}
	if old != nil {
		for _, p := range old.Panes {
			oldFirst[p.Pane] = p
		}
	}
	out := make([]state.CursorPane, 0, len(panes))
	for _, p := range panes {
		first := now
		was := p.Agent != ""
		if o, ok := oldFirst[p.ID]; ok {
			was = was || o.WasAgent || o.Agent != ""
			if (o.Agent == "") == (p.Agent == "") {
				first = o.First
			}
		}
		out = append(out, state.CursorPane{Pane: p.ID, Agent: p.Agent, Status: p.Status, CWD: p.CWD, First: first, WasAgent: was})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pane < out[j].Pane })
	return out
}

// samePanes reports whether two harness snapshots are identical.
func samePanes(old []state.CursorPane, next []state.CursorPane) bool {
	if len(old) != len(next) {
		return false
	}
	for i := range old {
		if old[i].Pane != next[i].Pane || old[i].Agent != next[i].Agent || old[i].Status != next[i].Status {
			return false
		}
	}
	return true
}
