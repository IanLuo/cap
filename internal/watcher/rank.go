package watcher

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/IanLuo/cap/internal/board"
	"github.com/IanLuo/cap/internal/state"
)

// recommend ranks every actable task: an unheld review before an unheld dev,
// oldest first within a kind. "Oldest" is fs's order, which is oldest first per
// project; across projects the board's project order is preserved. A task with
// an unconfirmed act younger than the gate is not recommended again.
func (w *Watcher) recommend(tasks []board.Task, rows []state.Row, now time.Time) ([]Recommendation, error) {
	all := map[string][]state.Act{}
	for _, r := range rows {
		if r.Kind != state.KindAct {
			continue
		}
		a, err := state.DecodeAct(r)
		if err != nil {
			return nil, err
		}
		all[a.Task] = append(all[a.Task], a)
	}

	type candidate struct {
		task board.Task
	}
	byTask := indexTasks(tasks)
	var actable []candidate
	for _, t := range tasks {
		if t.State != "open" {
			continue
		}
		if gated(all[t.Task], now, w.gate()) {
			continue
		}
		if t.Kind == "review" && awaitingFix(t, byTask) {
			continue
		}
		actable = append(actable, candidate{task: t})
	}
	sort.SliceStable(actable, func(i, j int) bool {
		return kindRank(actable[i].task.Kind) < kindRank(actable[j].task.Kind)
	})

	out := make([]Recommendation, 0, len(actable))
	for _, c := range actable {
		rec := Recommendation{
			Task:    c.task,
			Reason:  reason(c.task),
			Earlier: earlier(all[c.task.Task], now, w.gate()),
		}
		// Name the command Act would run, not the task's raw record: Act takes
		// the parent dev task's path, and asks when there is none.
		t := withPath(c.task, byTask)
		if t.ProjectPath == "" {
			rec.Gap = "task has no project_path; dispatch needs a working directory"
			out = append(out, rec)
			continue
		}
		choice, candidates, err := resolve(w.Store, c.task)
		if err != nil {
			return nil, fmt.Errorf("resolve knowledge for %s: %w", c.task.Task, err)
		}
		switch {
		case choice.Command != "":
			rec.Command = render(choice.Command, t)
			rec.Agent = choice.Agent
			rec.Knowledge = strconv.FormatInt(choice.ID, 10)
		case len(candidates) == 0:
			rec.Gap = "no knowledge record names a command"
		default:
			rec.Gap = "ambiguous knowledge: " + ids(candidates)
		}
		out = append(out, rec)
	}
	return out, nil
}

// withPath returns the task with the working directory cap would launch in: its
// own project_path, else its parent dev task's in the same project. An empty
// result means Act asks and launches nothing.
func withPath(t board.Task, byTask map[string]board.Task) board.Task {
	if t.ProjectPath != "" || t.ParentDev == "" {
		return t
	}
	if dev, ok := byTask[t.ParentDev]; ok && dev.Project == t.Project {
		t.ProjectPath = dev.ProjectPath
	}
	return t
}

func indexTasks(tasks []board.Task) map[string]board.Task {
	m := make(map[string]board.Task, len(tasks))
	for _, t := range tasks {
		m[t.Task] = t
	}
	return m
}

// awaitingFix reports whether a review's newest verdict is fail or blocked and
// its parent dev task has no newer dev-done. A newer dev-done is a fix: the
// review is actable again. No parent dev means cap cannot prove a fix is owed,
// so the review stays recommended.
func awaitingFix(review board.Task, byTask map[string]board.Task) bool {
	v := review.Agent
	if v.Role != "review" || v.At == nil || (v.Reported != "fail" && v.Reported != "blocked") {
		return false
	}
	dev, ok := byTask[review.ParentDev]
	if review.ParentDev == "" || !ok {
		return false
	}
	if dev.State == "done" {
		return true
	}
	d := dev.Agent
	if d.Reported != "dev-done" || d.At == nil {
		return true
	}
	return !d.At.After(*v.At)
}

func kindRank(kind string) int {
	if kind == "review" {
		return 0
	}
	return 1
}

func reason(t board.Task) string {
	why := "unheld " + t.Kind
	if t.Kind == "review" {
		why += ", before dev"
	} else {
		why += ", oldest first"
	}
	if t.Session != "" {
		why += ", lease lapsed"
	}
	return why
}

func gated(acts []state.Act, now time.Time, gate time.Duration) bool {
	latest := state.LatestAct(acts)
	if latest == nil || latest.Observed == state.ObservedConfirmed {
		return false
	}
	return now.Sub(latest.LaunchedAt) < gate
}

func earlier(acts []state.Act, now time.Time, gate time.Duration) string {
	var failed *state.Act
	for i := range acts {
		a := &acts[i]
		if a.Observed == state.ObservedConfirmed {
			continue
		}
		if now.Sub(a.LaunchedAt) < gate {
			continue
		}
		if failed == nil || a.LaunchedAt.After(failed.LaunchedAt) {
			failed = a
		}
	}
	if failed == nil {
		return "none"
	}
	return fmt.Sprintf("unconfirmed launch at %s (knowledge %s)", failed.LaunchedAt.UTC().Format(time.RFC3339), failed.Knowledge)
}

func ids(cs []knowledgeChoice) string {
	out := ""
	for i, c := range cs {
		if i > 0 {
			out += ", "
		}
		out += strconv.FormatInt(c.ID, 10)
	}
	return out
}
