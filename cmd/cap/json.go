package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/IanLuo/cap/internal/service"
	"github.com/IanLuo/cap/internal/state"
	"github.com/IanLuo/cap/internal/watcher"
)

// The JSON views are explicit. The wire format is part of the interface an
// agent calls, so it does not follow Go field names by accident.

type reportJSON struct {
	At              string               `json:"at"`
	Changed         bool                 `json:"changed"`
	Changes         []changeJSON         `json:"changes"`
	Confirmed       []state.Act          `json:"confirmed"`
	Pending         []state.Act          `json:"pending"`
	Recommendations []recommendationJSON `json:"recommendations"`
}

type changeJSON struct {
	Task    string `json:"task"`
	Project string `json:"project"`
	Reason  string `json:"reason"`
}

type recommendationJSON struct {
	Task      string `json:"task"`
	Project   string `json:"project"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	Command   string `json:"command,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Knowledge string `json:"knowledge,omitempty"`
	Gap       string `json:"gap,omitempty"`
	Earlier   string `json:"earlier,omitempty"`
}

type actJSON struct {
	Task      string `json:"task"`
	Command   string `json:"command"`
	Knowledge string `json:"knowledge"`
	Agent     string `json:"agent"`
	Launched  bool   `json:"launched"`
}

type statusJSON struct {
	Running            bool     `json:"running"`
	Loaded             bool     `json:"loaded"`
	PID                int      `json:"pid,omitempty"`
	Stale              bool     `json:"stale"`
	Label              string   `json:"label"`
	Plist              string   `json:"plist,omitempty"`
	LastTickAt         string   `json:"last_tick_at,omitempty"`
	LastTickAgeSeconds *float64 `json:"last_tick_age_seconds,omitempty"`
	LastError          string   `json:"last_error,omitempty"`
	Trace              string   `json:"trace"`
}

type actionJSON struct {
	Action  string `json:"action"`
	Running bool   `json:"running"`
	Loaded  bool   `json:"loaded"`
	PID     int    `json:"pid,omitempty"`
	Label   string `json:"label"`
}

type logsJSON struct {
	Path  string           `json:"path"`
	Lines []map[string]any `json:"lines"`
}

func newReport(r watcher.Report) reportJSON {
	out := reportJSON{
		At:              r.At.UTC().Format(time.RFC3339Nano),
		Changed:         r.Changed,
		Changes:         make([]changeJSON, 0, len(r.Changes)),
		Confirmed:       r.Confirmed,
		Pending:         r.Pending,
		Recommendations: make([]recommendationJSON, 0, len(r.Recommendations)),
	}
	if out.Confirmed == nil {
		out.Confirmed = []state.Act{}
	}
	if out.Pending == nil {
		out.Pending = []state.Act{}
	}
	for _, c := range r.Changes {
		out.Changes = append(out.Changes, changeJSON{Task: c.Task, Project: c.Project, Reason: c.Reason})
	}
	for _, rec := range r.Recommendations {
		out.Recommendations = append(out.Recommendations, recommendationJSON{
			Task: rec.Task.Task, Project: rec.Task.Project, Kind: rec.Task.Kind,
			Reason: rec.Reason, Command: rec.Command, Agent: rec.Agent,
			Knowledge: rec.Knowledge, Gap: rec.Gap, Earlier: rec.Earlier,
		})
	}
	return out
}

// newStatus reads the newest tick event. last_error is only current if the
// newest tick failed; an old error behind a newer good tick is history.
func newStatus(svc service.Launchd, st service.State, events []map[string]any, now time.Time) statusJSON {
	s := statusJSON{
		Running: st.Loaded && st.PID > 0,
		Loaded:  st.Loaded,
		PID:     st.PID,
		Label:   svc.Label,
		Plist:   svc.Plist,
		Trace:   tracePath(),
	}
	var last map[string]any
	for _, e := range events {
		if e["kind"] == "tick" {
			last = e
		}
	}
	if last == nil {
		s.Stale = s.Running
		return s
	}
	s.LastTickAt, _ = last["at"].(string)
	if t, err := time.Parse(time.RFC3339Nano, s.LastTickAt); err == nil {
		age := now.Sub(t).Seconds()
		s.LastTickAgeSeconds = &age
		s.Stale = s.Running && now.Sub(t) > 2*watcher.DefaultInterval
	}
	if last["result"] == "error" {
		s.LastError = fmt.Sprint(last["error"])
	}
	return s
}

func newAction(action string, svc service.Launchd, st service.State) actionJSON {
	return actionJSON{
		Action:  action,
		Running: st.Loaded && st.PID > 0,
		Loaded:  st.Loaded,
		PID:     st.PID,
		Label:   svc.Label,
	}
}

func (s statusJSON) Text() string {
	var b strings.Builder
	switch {
	case s.Running && s.Stale:
		fmt.Fprintf(&b, "watcher: running but not ticking (pid %d, label %s)\n", s.PID, s.Label)
	case s.Running:
		fmt.Fprintf(&b, "watcher: running (pid %d, label %s)\n", s.PID, s.Label)
	case s.Loaded:
		fmt.Fprintf(&b, "watcher: loaded, no pid yet (label %s)\n", s.Label)
	default:
		fmt.Fprintf(&b, "watcher: stopped (label %s)\n", s.Label)
	}
	if s.LastTickAt == "" {
		b.WriteString("last tick: none\n")
	} else if s.LastTickAgeSeconds != nil {
		fmt.Fprintf(&b, "last tick: %s (%s ago)\n", s.LastTickAt, shortDuration(*s.LastTickAgeSeconds))
	} else {
		fmt.Fprintf(&b, "last tick: %s\n", s.LastTickAt)
	}
	if s.LastError != "" {
		fmt.Fprintf(&b, "last error: %s\n", s.LastError)
	}
	return b.String()
}

func (a actionJSON) Text() string {
	switch {
	case a.Running:
		return fmt.Sprintf("watcher running (pid %d, label %s)\n", a.PID, a.Label)
	case a.Action == "stop":
		return fmt.Sprintf("watcher stopped (label %s)\n", a.Label)
	case a.Loaded:
		return fmt.Sprintf("watcher loaded; waiting for its first tick (label %s)\n", a.Label)
	default:
		return fmt.Sprintf("watcher not running (label %s)\n", a.Label)
	}
}

func shortDuration(seconds float64) string {
	return time.Duration(seconds * float64(time.Second)).Round(time.Second).String()
}
