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
	ClassReap    = "reap"
	ClassClose   = "close"
	ClassNudge   = "nudge"
	ClassBlocked = "blocked"
	// ClassInform reports a tick's events to the live claim holder. It is
	// news, never an instruction: it answers nothing and starts nothing.
	ClassInform = "inform"

	// maxNudgesPerRound bounds prompts to one stalled agent in one fs round.
	maxNudgesPerRound = 2
)

// actionRecommended is the trace result for a reap cap did not take because the
// agent in its pane is still working. It is a decision, not an action: no row
// is written and no side effect runs. The trace names the task and the pane.
const actionRecommended = "recommended"

// tier1 reads the transitions between the previous cursor and this tick and
// takes at most one action per task and class inside the gate. It never
// dispatches, never closes a pane, and never answers a blocked agent. It then
// tells the live claim holder what it did, in one prompt.
// A close is fs's, and only fs's: an fs-launched pane is reaped through fs when
// its round is over and its agent is no longer working. A pane with no launch
// row is the user's own (see humanOwned). A side effect that fails is recorded
// and reported, not retried.
// paneBaseline says the previous cursor holds no pane snapshot, so no pane can
// have moved; the reap keys off the board change instead, and still runs.
// Without a previous cursor at all there is nothing to compare: the tick is the
// baseline, and its one act is a reap for every project that still has an fs
// pane open.
func (w *Watcher) tier1(ctx context.Context, old *state.Cursor, tasks []board.Task, panes []agents.Pane, rows []state.Row, changes []Change, paneBaseline bool) ([]state.Action, []string, error) {
	actions, err := state.Actions(rows)
	if err != nil {
		return nil, nil, err
	}
	e := &engine{w: w, ctx: ctx, now: w.now(), old: old, tasks: tasks, panes: panes, rows: rows, actions: actions, spoken: map[string]bool{}, reaped: map[string]bool{}}

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
	e.launch = launchByPane
	if old == nil || paneBaseline {
		e.reapBaseline(paneByID)
	}
	if old == nil {
		e.inform(paneByID)
		return e.taken, e.refused, e.err
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

		if e.humanOwned(id) {
			// No fs launch row owns this pane: it is the user's own. cap
			// never closes it, never prompts it, never claims it. A blocked
			// one is announced, never answered.
			if p.Agent != "" && p.Status == "blocked" && transition {
				e.blockedOrphan(p)
			}
			continue
		}

		t, launched := e.launch[id]
		if !launched {
			// The launch row left with its task; fs still owns the close.
			continue
		}
		if p.Agent == "" {
			continue // fs closes its own panes; class 4 owns that
		}
		// fs keeps a working agent at its round's close. When that pane stops
		// working, the harness transition is the trigger to ask fs again: fs
		// closes an idle agent whose round is over. cap never closes the pane.
		if !e.spoken[t.Task] && transition && prev.Status == "working" && p.Status != "working" && roundOver(t) {
			e.reapOnce(t.Project, "pane "+p.ID+" stopped working, round over on task "+t.Task)
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

	e.announceClosed(changes, paneByID)
	e.reapClosed(changes, paneByID)
	// One voice: the holder hears the events first, the dispatcher that
	// dispatched a closed round hears that round, and the toasts stand down
	// when either did. Without them the toast is the fallback.
	e.postToasts(e.inform(paneByID))
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
	// launch maps a pane to the open fs task whose launch row owns it. A pane
	// absent from this map (and from the previous cursor) is human-owned.
	launch  map[string]board.Task
	taken   []state.Action
	refused []string
	err     error
	// orphanAsked is this tick's own answer to hasQuestion for the one
	// board-wide gap: rows were read at tick start and cannot see a question
	// written moments earlier in the same tick.
	orphanAsked bool
	// spoken holds every task cap nudged or announced this tick. The
	// harness-transition reap skips their panes: cap does not reap a pane it
	// just asked the human or the agent to answer.
	spoken map[string]bool
	// reaped holds every project this tick already asked fs to reap. Several
	// transitions can end one project's round; fs is asked once.
	reaped map[string]bool
	// events are what the tick tells the claim holder: a round finished, a
	// reviewer said blocked, an agent is blocked. They are not actions; the
	// tick changes nothing because of them.
	events []Event
	// toasts are the human notifications the events would raise, held until
	// the tick knows whether the holder was informed of the same events.
	toasts []toast
}

// Event is one thing worth telling the claim holder. Kind is "finished",
// "blocked" or "review blocked". Task is empty for a pane no task owns; Where
// then names what the agent was working in.
type Event struct {
	Kind    string
	Project string
	Task    string
	Pane    string
	Where   string
}

// toast is one notification, held back until the tick knows whether the claim
// holder was told the same event.
type toast struct {
	title, body string
}

// reapOnce is the one reap cap takes for a project in a tick, whichever
// transition led to it. The 90 s gate still holds across ticks.
func (e *engine) reapOnce(project, why string) {
	if e.w.Reap == nil || e.reaped[project] {
		return
	}
	e.reaped[project] = true
	e.perform(state.Action{
		Class:   ClassReap,
		Target:  project,
		Project: project,
		Trigger: why,
	}, func() error { return e.w.Reap.Reap(e.ctx, project) })
}

// reapBaseline is the first observation's one act: one fs reap for every
// project that still has an fs launch row whose pane is open in the harness.
// The cursor cannot witness a round that ended while cap was not running, so
// the open pane is the evidence. Only launch rows are iterated: a pane no row
// owns is human-owned and is never named here (see humanOwned).
func (e *engine) reapBaseline(paneByID map[string]agents.Pane) {
	for _, t := range e.tasks {
		if t.Process.Pane == "" {
			continue
		}
		if _, open := paneByID[t.Process.Pane]; !open {
			continue
		}
		e.reapOnce(t.Project, "first read: task "+t.Task+", pane "+t.Process.Pane+" still open")
	}
}

// roundOver reports whether the board says the task's round has ended: the task
// is closed, or its session's newest review word is fail or blocked.
func roundOver(t board.Task) bool {
	if t.State == "done" {
		return true
	}
	return t.Agent.Role == "review" && (t.Agent.Reported == "fail" || t.Agent.Reported == "blocked")
}

// humanOwned reports whether no fs launch row owns the pane. Such a pane is the
// user's own: cap never closes it, never prompts it, never claims it. This is
// the one place the rule lives; every path that resolves a pane from the
// harness asks it first. The evidence of a launch row is the open task fs
// started there now, or the row the previous cursor kept for a task that has
// just left the board: its launch row goes with the round, but fs still owns
// the pane. The inform path is not a pane resolution — it addresses the claim's
// pane, which the holder declared — so its rule is unchanged.
func (e *engine) humanOwned(paneID string) bool {
	if _, ok := e.launch[paneID]; ok {
		return false
	}
	if e.old != nil {
		for _, t := range e.old.Tasks {
			if t.Pane == paneID {
				return false
			}
		}
	}
	return true
}

// working reports whether the harness still shows an agent at work in the
// pane. A pane with no agent, or one whose agent is idle, blocked, done, gone
// or unknown, is not working: its round's pane may be reaped.
func working(p agents.Pane) bool {
	return p.Agent != "" && p.Status == "working"
}

// recommendReap records a reap cap did not take: the round is over but the
// agent is still working, so fs kept the pane. The line names the task and the
// pane and changes nothing; the harness transition out of working is the
// trigger to act on the next tick.
func (e *engine) recommendReap(project, task, pane, why string) {
	e.traceAction(state.Action{
		Class:   ClassReap,
		Target:  project,
		Project: project,
		Task:    task,
		Pane:    pane,
		Trigger: why,
	}, actionRecommended)
}

// announceClosed records a dev task that just left the board and tells its
// dispatcher. fs completes a dev task only through a review that ran its
// declared checks, so the round is done and the review passed. The record is
// written first and survives the popup; the prompt goes to the pane fs named
// as the caller when it launched the task, so the session that dispatched the
// work is told without polling. The review task that closed the same round is
// neither recorded nor announced again.
func (e *engine) announceClosed(changes []Change, paneByID map[string]agents.Pane) {
	oldByTask := map[string]state.CursorTask{}
	for _, t := range e.old.Tasks {
		oldByTask[t.Task] = t
	}
	byTask := make(map[string]board.Task, len(e.tasks))
	for _, t := range e.tasks {
		byTask[t.Task] = t
	}
	for _, c := range changes {
		if !strings.HasPrefix(c.Reason, "no longer open") {
			continue
		}
		o, ok := oldByTask[c.Task]
		if !ok || o.Kind != "dev" {
			continue
		}
		e.events = append(e.events, Event{Kind: "finished", Project: c.Project, Task: c.Task})
		if e.announceRound(c, o, byTask[c.Task], paneByID) {
			continue // the dispatcher was told; the human needs no second popup
		}
		e.toasts = append(e.toasts, toast{title: "cap: round closed", body: c.Project + " " + c.Task + " done, review passed"})
	}
}

// announceRound writes the closed round's durable record, then tells the
// dispatcher through the pane fs recorded as its caller. It returns true when
// the news reached a pane, or was already on record, so the toast knows whether
// it is still the announcement. The record is written before any prompt: when
// the write fails, the trace says so and no pane is addressed. A round already
// on record is not written or announced a second time.
func (e *engine) announceRound(c Change, o state.CursorTask, t board.Task, paneByID map[string]agents.Pane) bool {
	rec := roundRecord(c, o, t, e.now)
	a := state.Action{
		Class:   ClassInform,
		Target:  c.Task,
		Project: c.Project,
		Task:    c.Task,
		Trigger: "round closed for task " + c.Task,
	}

	recorded, err := state.RoundRecorded(e.rows, c.Task)
	if err != nil {
		e.err = err
		e.tracePath(a, state.ActionFailed+": "+err.Error(), "toast", "cap.round read failed; no prompt")
		return false
	}
	if recorded {
		// The tick that wrote the row announced the round. A replay after a
		// failed cursor write must not write a second row or speak twice.
		e.tracePath(a, state.ActionDone, "recorded", "cap.round already on record for this round")
		return true
	}
	text, err := state.Encode(rec)
	if err != nil {
		e.err = err
		e.tracePath(a, state.ActionFailed+": "+err.Error(), "toast", "cap.round encode failed; no prompt")
		return false
	}
	if _, err := e.w.Store.Put(state.KindRound, state.Scope(c.Project), "round closed "+c.Task, text); err != nil {
		e.err = fmt.Errorf("write cap.round: %w", err)
		e.tracePath(a, state.ActionFailed+": "+e.err.Error(), "toast", "cap.round write failed; the caller was not prompted")
		return false
	}

	caller := t.Process.CallerPane
	a.Pane = caller
	switch {
	case caller == "":
		e.tracePath(a, state.ActionDone, "toast", "fs recorded no caller pane")
		return false
	case caller == t.Process.Pane:
		e.tracePath(a, state.ActionDone, "toast", "caller pane is the launched pane")
		return false
	case e.humanOwned(caller):
		e.tracePath(a, state.ActionDone, "toast", "no fs launch row owns the caller pane")
		return false
	}
	p, listed := paneByID[caller]
	if !listed || p.Agent == "" || p.Status == "blocked" {
		e.tracePath(a, state.ActionDone, "toast", "the caller pane is unlisted, empty or blocked")
		return false
	}
	holder, err := e.claimPane(paneByID)
	if err != nil {
		e.err = err
		e.tracePath(a, state.ActionFailed+": "+err.Error(), "toast", "cap.claim read failed; no prompt")
		return false
	}
	if holder == caller {
		e.tracePath(a, state.ActionDone, "holder", "the caller is the claim holder this tick informs")
		return false // no second prompt; the toast stands down if that one lands
	}
	prompt := roundText(rec)
	return e.announceTo(a, func() error { return e.w.Agents.Prompt(e.ctx, caller, prompt) }, "caller", "dispatcher pane from fs caller_pane")
}

// announceTo is performUngated for the closed-round announcement: it records
// the attempt, runs it, and writes one trace line carrying the path the
// announcement took and why. A one-off news prompt needs no 90 s gate; the
// round record already keeps a repeated tick from announcing twice.
func (e *engine) announceTo(a state.Action, run func() error, path, why string) bool {
	a.At = e.now
	a.Outcome = state.ActionAttempted
	id, err := e.put(a)
	if err != nil {
		e.err = err
		e.tracePath(a, state.ActionFailed+": "+err.Error(), path, why)
		return false
	}
	a.ID = id
	if err := run(); err != nil {
		failed := state.ActionFailed + ": " + err.Error()
		e.finish(a, failed)
		e.refused = append(e.refused, a.Class+" "+a.Target+" failed: "+err.Error())
		e.tracePath(a, failed, path, why)
		return false
	}
	e.finish(a, state.ActionDone)
	e.tracePath(a, state.ActionDone, path, why)
	return true
}

// roundRecord builds the durable row for a closed round: its project and task,
// the session that did the work, the review verdict, and the commit the newest
// dev-done report named. The goal is kept so a later session can find the row
// by what the work was.
func roundRecord(c Change, o state.CursorTask, t board.Task, now time.Time) state.Round {
	rec := state.Round{
		Project: c.Project,
		Task:    c.Task,
		Round:   t.Round,
		Goal:    t.Goal,
		Verdict: "pass", // a dev task leaves the board only after its review passed
		At:      now,
	}
	if rec.Goal == "" {
		rec.Goal = o.Goal
	}
	rec.Session = t.Session
	if rec.Session == "" {
		rec.Session = o.Session
	}
	rec.Commit = commitOf(t)
	if rec.Session == "" {
		rec.Session = evidenceSession(t)
	}
	return rec
}

// commitOf is the commit the dev task's newest dev-done report named, falling
// back to the newest report's digest. fs evidence is newest first.
func commitOf(t board.Task) string {
	any := ""
	for _, ev := range t.Evidence {
		if ev.Digest == "" {
			continue
		}
		if any == "" {
			any = ev.Digest
		}
		if ev.State == "dev-done" {
			return ev.Digest
		}
	}
	return any
}

// evidenceSession is the session that wrote the newest report. It is the
// fallback when the board no longer names a holder for the closed task.
func evidenceSession(t board.Task) string {
	for _, ev := range t.Evidence {
		if ev.Session != "" {
			return ev.Session
		}
	}
	return ""
}

// roundText renders the closed-round announcement for the dispatcher. The task
// is named by goal, never by id: the reader is deciding what to do next.
func roundText(rec state.Round) string {
	subject := rec.Goal
	if subject == "" {
		subject = rec.Task
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cap: round closed %s: %q review passed", rec.Project, subject)
	if rec.Commit != "" {
		fmt.Fprintf(&b, ", commit %s", rec.Commit)
	}
	b.WriteString(". Report only; nothing here asks a question.\n")
	return b.String()
}

// postToasts sends the notifications the events would raise. One voice: when
// the tick informed a live holder of these events, or prompted the dispatcher
// the closed round belonged to, the human is spared the duplicate. With
// neither, the toast is the announcement.
func (e *engine) postToasts(informed bool) {
	if informed {
		return
	}
	for _, n := range e.toasts {
		if err := e.w.Agents.Notify(e.ctx, n.title, n.body); err != nil {
			e.refused = append(e.refused, "notify "+n.title+": "+err.Error())
		}
	}
}

// claimPane is the one pane this tick's inform will address, or empty when
// there is no such address. The announce uses it to leave the claim holder's
// prompt as the single voice for a round it would otherwise duplicate.
func (e *engine) claimPane(paneByID map[string]agents.Pane) (string, error) {
	claim, err := state.OpenClaim(e.rows)
	if err != nil {
		return "", err
	}
	if claim == nil || claim.Pane == "" {
		return "", nil
	}
	p, listed := paneByID[claim.Pane]
	if !listed || p.Agent == "" || p.Status == "blocked" {
		return "", nil
	}
	return claim.Pane, nil
}

// inform tells the claim holder what this tick saw: one prompt, one line
// per event, each naming its kind, project and task by goal. It is the one
// message on the clock path addressed to the claim holder. It returns true only
// when the prompt was sent, so the toasts know whether to stand down. It reports; it
// never instructs, never answers a blocked agent (so it never prompts a pane
// the harness shows as blocked), and never changes what an action does.
// The lease gates the write verbs, not this address: a lapsed claim still names
// where its holder lives, until a takeover writes a new one.
func (e *engine) inform(paneByID map[string]agents.Pane) bool {
	if len(e.events) == 0 {
		return false
	}
	claim, err := state.OpenClaim(e.rows)
	if err != nil {
		e.err = err
		return false
	}
	if claim == nil || claim.Pane == "" {
		return false
	}
	p, listed := paneByID[claim.Pane]
	if !listed || p.Agent == "" || p.Status == "blocked" {
		return false
	}
	a := state.Action{
		Class:   ClassInform,
		Target:  claim.Session,
		Pane:    claim.Pane,
		Trigger: fmt.Sprintf("%d event(s) this tick", len(e.events)),
	}
	text := e.informText()
	return e.performUngated(a, func() error { return e.w.Agents.Prompt(e.ctx, claim.Pane, text) })
}

// informText renders the tick's events for the claim holder. Tasks are named by
// goal, never by id: the reader of this line is deciding what to do next.
func (e *engine) informText() string {
	goals := make(map[string]string, len(e.tasks))
	if e.old != nil {
		goals = make(map[string]string, len(e.tasks)+len(e.old.Tasks))
		for _, t := range e.old.Tasks {
			goals[t.Task] = t.Goal
		}
	}
	for _, t := range e.tasks {
		goals[t.Task] = t.Goal
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cap: %d event(s) this tick. Report only; nothing here asks a question.\n", len(e.events))
	for _, ev := range e.events {
		project := ev.Project
		if project == "" {
			project = "cap"
		}
		subject := goals[ev.Task]
		if subject == "" {
			subject = ev.Where
		}
		if ev.Kind == "finished" {
			fmt.Fprintf(&b, "- finished %s: %q review passed\n", project, subject)
			continue
		}
		fmt.Fprintf(&b, "- %s %s: %q in pane %s\n", ev.Kind, project, subject, ev.Pane)
	}
	return b.String()
}

// reapClosed calls fs reap once per project whose round just ended and whose
// fs-launched pane no longer holds a working agent. fs decides which pane to
// close; cap never closes an fs pane. A close needs both: a launch row for a
// task whose round is over, and an agent that is no longer working. A lease
// lapse alone is not a round over. When the round is over and the agent still
// works, the tick records a recommendation in the trace and leaves the pane;
// the harness transition out of working is the trigger to reap it.
func (e *engine) reapClosed(changes []Change, paneByID map[string]agents.Pane) {
	if e.w.Reap == nil || e.old == nil {
		return
	}
	oldByTask := map[string]state.CursorTask{}
	for _, t := range e.old.Tasks {
		oldByTask[t.Task] = t
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
		p, open := paneByID[o.Pane]
		if !open || e.humanOwned(o.Pane) {
			continue
		}
		if working(p) {
			e.recommendReap(c.Project, c.Task, o.Pane, "D1: round closed for task "+c.Task+", agent still working")
			continue
		}
		e.reapOnce(c.Project, "round closed for task "+c.Task+", pane "+o.Pane+" still open")
	}

	// A task still on the board whose round ended with a review verdict.
	for _, t := range e.tasks {
		if t.Process.Pane == "" {
			continue
		}
		p, open := paneByID[t.Process.Pane]
		if !open || e.humanOwned(t.Process.Pane) {
			continue
		}
		o, known := oldByTask[t.Task]
		if !known || !reviewVerdictEnded(o, t) {
			continue
		}
		if working(p) {
			e.recommendReap(t.Project, t.Task, t.Process.Pane, "D1: review verdict "+t.Agent.Reported+" on task "+t.Task+", agent still working")
		} else {
			e.reapOnce(t.Project, "review verdict "+t.Agent.Reported+" on task "+t.Task+", pane "+t.Process.Pane+" still open")
		}
		// A recoverable fail is the developer's business. A reviewer that
		// says blocked cannot go on without a human.
		if t.Agent.Reported == "blocked" {
			e.events = append(e.events, Event{Kind: "review blocked", Project: t.Project, Task: t.Task, Pane: t.Process.Pane})
		}
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

func (e *engine) nudge(t board.Task, p agents.Pane) {
	a := state.Action{
		Class:   ClassNudge,
		Target:  t.Task,
		Project: t.Project,
		Task:    t.Task,
		Pane:    p.ID,
		Round:   t.Round,
		Trigger: "lapsed claim, agent idle",
	}
	if n := state.CountActionsInRound(append(e.actions, e.taken...), ClassNudge, a.Target, t.Round); n >= maxNudgesPerRound {
		e.refused = append(e.refused, fmt.Sprintf("nudge %s: round %d already nudged %d times", t.Task, t.Round, n))
		e.traceAction(a, actionRefused)
		return
	}
	text := nudgeText(t)
	// A pane cap just spoke to is not reaped in the same tick: fs closes an
	// idle agent whose lease lapsed, and the prod must get its chance to renew.
	e.spoken[a.Target] = true
	e.perform(a, func() error { return e.w.Agents.Prompt(e.ctx, p.ID, text) })
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
	a := state.Action{
		Class:   ClassBlocked,
		Target:  t.Task,
		Project: t.Project,
		Task:    t.Task,
		Pane:    p.ID,
		Trigger: "blocked: agent is waiting on a prompt",
	}
	// Announcing a blocked agent must not be undone in the same tick: the
	// human answers, and the pane waits for that answer.
	e.spoken[t.Task] = true
	missing := a.Trigger
	if !e.hasQuestion(t.Task, missing) {
		if err := e.ask(t.Task, t.Project, state.Scope(t.Project), missing); err != nil {
			e.err = err
			e.traceAction(a, state.ActionFailed+": "+err.Error())
			return
		}
	}
	body := fmt.Sprintf("%s %s — %s (pane %s)", t.Project, t.Task, t.Goal, p.ID)
	e.events = append(e.events, Event{Kind: "blocked", Project: t.Project, Task: t.Task, Pane: p.ID})
	e.toasts = append(e.toasts, toast{title: "cap: agent blocked", body: body})
	e.traceAction(a, state.ActionDone)
}

func (e *engine) blockedOrphan(p agents.Pane) {
	a := state.Action{
		Class:   ClassBlocked,
		Target:  p.ID,
		Pane:    p.ID,
		Trigger: "blocked: agent is waiting on a prompt",
	}
	missing := a.Trigger
	if !e.orphanAsked && !e.hasQuestion("", missing) {
		if err := e.ask("", "", state.CursorScope, missing); err != nil {
			e.err = err
			e.traceAction(a, state.ActionFailed+": "+err.Error())
			return
		}
		e.orphanAsked = true
	}
	body := fmt.Sprintf("unowned agent blocked in %s (pane %s)", p.CWD, p.ID)
	e.events = append(e.events, Event{Kind: "blocked", Pane: p.ID, Where: p.CWD})
	e.toasts = append(e.toasts, toast{title: "cap: agent blocked", body: body})
	e.traceAction(a, state.ActionDone)
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
		e.traceAction(a, actionRefused)
		return
	}
	e.runAction(a, run)
}

// performUngated is perform without the 90 s gate. The inform needs no gate of
// its own: a tick sends at most one, and only when it saw an event.
func (e *engine) performUngated(a state.Action, run func() error) bool {
	return e.runAction(a, run)
}

// runAction records the attempt, runs the side effect, and closes the attempt
// with its outcome. It reports whether the side effect succeeded.
func (e *engine) runAction(a state.Action, run func() error) bool {
	a.At = e.now
	a.Outcome = state.ActionAttempted
	id, err := e.put(a)
	if err != nil {
		e.err = err
		e.traceAction(a, state.ActionFailed+": "+err.Error())
		return false
	}
	a.ID = id
	if run != nil {
		if err := run(); err != nil {
			e.finish(a, state.ActionFailed+": "+err.Error())
			e.refused = append(e.refused, a.Class+" "+a.Target+" failed: "+err.Error())
			e.traceAction(a, state.ActionFailed+": "+err.Error())
			return false
		}
	}
	e.finish(a, state.ActionDone)
	e.traceAction(a, state.ActionDone)
	return true
}

// actionRefused is the trace result for a decision the gate or a round bound
// stopped before any side effect.
const actionRefused = "refused"

// traceAction writes one line per tier-1 decision: its class, what it named,
// why, and what happened. The trace never gates a decision.
func (e *engine) traceAction(a state.Action, result string) {
	e.w.trace(e.actionLine(a, result))
}

// tracePath is traceAction with the path a closed-round announcement took — the
// dispatcher's pane ("caller"), the claim holder ("holder") or the human toast —
// and why it took it.
func (e *engine) tracePath(a state.Action, result, path, why string) {
	ev := e.actionLine(a, result)
	ev["path"] = path
	ev["why"] = why
	e.w.trace(ev)
}

func (e *engine) actionLine(a state.Action, result string) map[string]any {
	return map[string]any{
		"kind":    "action",
		"at":      e.now.UTC().Format(time.RFC3339Nano),
		"class":   a.Class,
		"target":  a.Target,
		"project": a.Project,
		"task":    a.Task,
		"pane":    a.Pane,
		"trigger": a.Trigger,
		"result":  result,
	}
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

// paneSnapshot carries the harness read into the cursor. The comparison fields
// are what a later tick needs to see a transition; the cursor needs no history
// of its own beyond At.
func paneSnapshot(panes []agents.Pane) []state.CursorPane {
	out := make([]state.CursorPane, 0, len(panes))
	for _, p := range panes {
		out = append(out, state.CursorPane{Pane: p.ID, Agent: p.Agent, Status: p.Status, CWD: p.CWD})
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
