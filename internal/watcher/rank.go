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
	var actable []candidate
	for _, t := range tasks {
		if t.State != "open" {
			continue
		}
		if gated(all[t.Task], now, w.gate()) {
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
		choice, candidates, err := resolve(w.Store, c.task)
		if err != nil {
			return nil, fmt.Errorf("resolve knowledge for %s: %w", c.task.Task, err)
		}
		switch {
		case choice.Command != "":
			rec.Command = render(choice.Command, c.task)
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
