package watcher

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// Refusal is a named instruction cap declines to run. It is a normal outcome,
// not a crash: no command launched and no act written.
type Refusal struct{ Message string }

func (r *Refusal) Error() string { return r.Message }

// IsRefusal reports whether err is a refusal rather than a failure.
func IsRefusal(err error) bool {
	var r *Refusal
	return errors.As(err, &r)
}

// ActResult is what one instruction did: launched one command, or asked one
// question and launched nothing.
type ActResult struct {
	Launched  bool
	Task      string
	Command   string
	Agent     string
	Knowledge string
	Question  string
}

// Act is the only path that launches. It reads the board once, refuses inside
// the gate, resolves exactly one command from knowledge, launches it detached
// in the task's project, and records one open cap.act.
func (d *Dispatcher) Act(ctx context.Context, tid string) (ActResult, error) {
	now := d.now()
	tasks, err := d.Board.Status(ctx)
	if err != nil {
		return ActResult{}, fmt.Errorf("read board: %w", err)
	}
	var task board.Task
	found := false
	for _, t := range tasks {
		if t.Task == tid {
			task, found = t, true
			break
		}
	}
	if !found {
		return ActResult{}, &Refusal{Message: fmt.Sprintf("no task %s on the board", tid)}
	}
	if task.State != "open" {
		return ActResult{}, &Refusal{Message: fmt.Sprintf("task %s is not actable: state=%s", tid, task.State)}
	}

	rows, err := d.Store.Recent(state.Scope(task.Project), rowLimit)
	if err != nil {
		return ActResult{}, fmt.Errorf("read cap state for %s: %w", task.Project, err)
	}
	acts, err := allActs(rows, tid)
	if err != nil {
		return ActResult{}, err
	}
	previous, err := state.Acts(rows, tid)
	if err != nil {
		return ActResult{}, err
	}
	if latest := state.LatestAct(acts); latest != nil && now.Sub(latest.LaunchedAt) < d.gate() {
		msg := fmt.Sprintf("task %s was launched at %s by knowledge %s; refused inside the %s gate",
			tid, latest.LaunchedAt.UTC().Format(time.RFC3339), latest.Knowledge, d.gate())
		d.trace(map[string]any{"kind": "act", "at": now.UTC().Format(time.RFC3339Nano), "task": tid, "result": "refused", "why": "gate"})
		return ActResult{}, &Refusal{Message: msg}
	}

	questions, err := state.Questions(rows, tid)
	if err != nil {
		return ActResult{}, err
	}
	if task.ProjectPath == "" {
		return d.ask(task, "task has no project_path; dispatch needs a working directory", questions, now)
	}

	choice, candidates, err := resolve(d.Store, task)
	if err != nil {
		return ActResult{}, fmt.Errorf("resolve knowledge for %s: %w", tid, err)
	}
	switch {
	case choice.Command == "" && len(candidates) == 0:
		return d.ask(task, "no knowledge record names a command for "+task.Kind+" work", questions, now)
	case choice.Command == "":
		return d.ask(task, "ambiguous knowledge: "+ids(candidates), questions, now)
	}

	command := render(choice.Command, task)
	act := state.Act{
		Task:       task.Task,
		Project:    task.Project,
		Kind:       task.Kind,
		Command:    command,
		Agent:      choice.Agent,
		Knowledge:  strconv.FormatInt(choice.ID, 10),
		LaunchedAt: now,
		Observed:   state.ObservedUnconfirmed,
	}

	// Record the act before starting the command: a crash between the two
	// leaves an open act that still holds the 90 s gate, rather than a launch
	// nothing knows about. A failed start closes the record again.
	text, err := state.Encode(act)
	if err != nil {
		return ActResult{}, err
	}
	id, err := d.Store.Put(state.KindAct, state.Scope(task.Project), actTitle(act), text)
	if err != nil {
		return ActResult{}, fmt.Errorf("write cap.act: %w", err)
	}
	if err := d.Launcher.Launch(task.ProjectPath, command); err != nil {
		if cerr := d.Store.Close(id); cerr != nil {
			return ActResult{}, fmt.Errorf("launch %q in %s: %w (and close cap.act %d: %v)", command, task.ProjectPath, err, id, cerr)
		}
		d.trace(map[string]any{"kind": "act", "at": now.UTC().Format(time.RFC3339Nano), "task": tid, "result": "launch-failed", "error": err.Error()})
		return ActResult{}, fmt.Errorf("launch %q in %s: %w", command, task.ProjectPath, err)
	}
	for _, p := range previous {
		if err := d.Store.Close(p.ID); err != nil {
			return ActResult{}, fmt.Errorf("close earlier cap.act %d: %w", p.ID, err)
		}
	}
	for _, q := range questions {
		if err := d.Store.Close(q.ID); err != nil {
			return ActResult{}, fmt.Errorf("close cap.question %d: %w", q.ID, err)
		}
	}

	d.trace(map[string]any{
		"kind": "act", "at": now.UTC().Format(time.RFC3339Nano), "task": tid,
		"result": "launched", "command": command, "knowledge": act.Knowledge, "agent": act.Agent,
	})
	return ActResult{Launched: true, Task: tid, Command: command, Agent: act.Agent, Knowledge: act.Knowledge}, nil
}

// ask writes one question per task and gap. A second instruction for the same
// gap opens no second question and launches nothing.
func (d *Dispatcher) ask(task board.Task, missing string, open []state.Question, now time.Time) (ActResult, error) {
	for _, q := range open {
		if q.Missing == missing {
			d.trace(map[string]any{"kind": "act", "at": now.UTC().Format(time.RFC3339Nano), "task": task.Task, "result": "refused", "why": "question-open"})
			return ActResult{Task: task.Task, Question: missing},
				&Refusal{Message: fmt.Sprintf("question %d is already open: %s", q.ID, missing)}
		}
	}
	text, err := state.Encode(state.Question{Task: task.Task, Project: task.Project, Missing: missing, At: now})
	if err != nil {
		return ActResult{}, err
	}
	id, err := d.Store.Put(state.KindQuestion, state.Scope(task.Project), "knowledge gap for "+task.Task, text)
	if err != nil {
		return ActResult{}, fmt.Errorf("write cap.question: %w", err)
	}
	d.trace(map[string]any{"kind": "act", "at": now.UTC().Format(time.RFC3339Nano), "task": task.Task, "result": "question", "missing": missing})
	return ActResult{Task: task.Task, Question: missing},
		&Refusal{Message: fmt.Sprintf("asked question %d: %s", id, missing)}
}

// allActs returns every act for a task, open or closed, so the gate cannot be
// escaped by closing a record.
func allActs(rows []state.Row, tid string) ([]state.Act, error) {
	var out []state.Act
	for _, r := range rows {
		if r.Kind != state.KindAct {
			continue
		}
		a, err := state.DecodeAct(r)
		if err != nil {
			return nil, err
		}
		if a.Task == tid {
			out = append(out, a)
		}
	}
	return out, nil
}

func actTitle(a state.Act) string { return "act for " + a.Task }
