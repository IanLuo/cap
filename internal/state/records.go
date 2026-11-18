package state

import (
	"encoding/json"
	"fmt"
	"time"
)

// Act is one launched command: the fs task it serves, the command, the agent
// kind, the knowledge record that named it, when it launched, and what was
// observed later. One open act per task.
type Act struct {
	ID          int64      `json:"-"`
	Task        string     `json:"task"`
	Project     string     `json:"project"`
	Kind        string     `json:"kind"`
	Command     string     `json:"command"`
	Agent       string     `json:"agent"`
	Knowledge   string     `json:"knowledge"`
	LaunchedAt  time.Time  `json:"launched_at"`
	Observed    string     `json:"observed"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	Session     string     `json:"session,omitempty"`
}

// The two observed results. A launch starts unconfirmed; a later tick that sees
// a holder confirms it. cap never marks a task failed.
const (
	ObservedUnconfirmed = "unconfirmed"
	ObservedConfirmed   = "confirmed"
)

// Question is one knowledge gap: what is missing and which task needs it.
// One open question per task and gap.
type Question struct {
	ID      int64     `json:"-"`
	Task    string    `json:"task"`
	Project string    `json:"project"`
	Missing string    `json:"missing"`
	At      time.Time `json:"at"`
}

// Cursor is the last board and harness snapshot. It is written only when the
// board or the harness changes, and there is at most one open cursor.
type Cursor struct {
	ID    int64        `json:"-"`
	At    time.Time    `json:"at"`
	Tasks []CursorTask `json:"tasks"`
	Panes []CursorPane `json:"panes,omitempty"`
}

// CursorPane is one pane's comparison fields: the agent it runs, its state, the
// tick it was first seen in that class, and whether it has ever run an agent.
// First anchors the close grace period, so a pane is never closed on elapsed
// time alone. WasAgent marks an orphan: a pane that held an agent and lost it.
// A pane that never held one is a human terminal, never cap's to close.
type CursorPane struct {
	Pane     string    `json:"pane"`
	Agent    string    `json:"agent,omitempty"`
	Status   string    `json:"status"`
	CWD      string    `json:"cwd,omitempty"`
	First    time.Time `json:"first"`
	WasAgent bool      `json:"was_agent,omitempty"`
}

// Action is one tier-1 act: what cap did without an instruction, why, to what,
// and when. It is written before the side effect; a later row carries the
// outcome. One action per class and target per gate.
type Action struct {
	ID      int64     `json:"-"`
	Class   string    `json:"class"`
	Target  string    `json:"target"`
	Project string    `json:"project,omitempty"`
	Task    string    `json:"task,omitempty"`
	Pane    string    `json:"pane,omitempty"`
	Round   int       `json:"round,omitempty"`
	Trigger string    `json:"trigger"`
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
}

// The two action outcomes. An action starts attempted; it ends done or failed.
const (
	ActionAttempted = "attempted"
	ActionDone      = "done"
	ActionFailed    = "failed"
)

// CursorTask is one open task's comparison fields: holder state, lease, newest
// report time, and the pane fs launched for it. Project is identity, carried so
// a later tick knows which cap: scopes to read.
type CursorTask struct {
	Task         string     `json:"task"`
	Project      string     `json:"project"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	Goal         string     `json:"goal,omitempty"`
	Session      string     `json:"session,omitempty"`
	Pane         string     `json:"pane,omitempty"`
	LeaseExpires *time.Time `json:"lease_expires,omitempty"`
	LeaseLive    bool       `json:"lease_live"`
	NewestReport *time.Time `json:"newest_report,omitempty"`
}

// Encode renders a record as the text memstore stores.
func Encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeAct reads one cap.act row.
func DecodeAct(r Row) (Act, error) {
	var a Act
	if err := json.Unmarshal([]byte(r.Text), &a); err != nil {
		return Act{}, fmt.Errorf("decode cap.act %d: %w", r.ID, err)
	}
	a.ID = r.ID
	return a, nil
}

// DecodeQuestion reads one cap.question row.
func DecodeQuestion(r Row) (Question, error) {
	var q Question
	if err := json.Unmarshal([]byte(r.Text), &q); err != nil {
		return Question{}, fmt.Errorf("decode cap.question %d: %w", r.ID, err)
	}
	q.ID = r.ID
	return q, nil
}

// DecodeAction reads one cap.action row.
func DecodeAction(r Row) (Action, error) {
	var a Action
	if err := json.Unmarshal([]byte(r.Text), &a); err != nil {
		return Action{}, fmt.Errorf("decode cap.action %d: %w", r.ID, err)
	}
	a.ID = r.ID
	return a, nil
}

// Actions returns every action row, newest first.
func Actions(rows []Row) ([]Action, error) {
	var out []Action
	for _, r := range rows {
		if r.Kind != KindAction {
			continue
		}
		a, err := DecodeAction(r)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// LatestAction returns the newest action for one class and target, or nil.
// Ties break on row id so the answer is deterministic.
func LatestAction(actions []Action, class, target string) *Action {
	var latest *Action
	for i := range actions {
		a := &actions[i]
		if a.Class != class || a.Target != target {
			continue
		}
		if latest == nil || a.At.After(latest.At) || (a.At.Equal(latest.At) && a.ID > latest.ID) {
			latest = a
		}
	}
	return latest
}

// LatestActionInRound returns the newest action for one class and target in one
// fs round. It bounds a nudge to two per round across restarts.
func LatestActionInRound(actions []Action, class, target string, round int) *Action {
	var latest *Action
	for i := range actions {
		a := &actions[i]
		if a.Class != class || a.Target != target || a.Round != round {
			continue
		}
		if latest == nil || a.At.After(latest.At) || (a.At.Equal(latest.At) && a.ID > latest.ID) {
			latest = a
		}
	}
	return latest
}

// CountActionsInRound counts completed actions for one class, target and round.
// The attempted row of a finished action is history, not a second action.
func CountActionsInRound(actions []Action, class, target string, round int) int {
	n := 0
	for _, a := range actions {
		if a.Class == class && a.Target == target && a.Round == round && a.Outcome != ActionAttempted {
			n++
		}
	}
	return n
}

// DecodeCursor reads one cap.cursor row.
func DecodeCursor(r Row) (Cursor, error) {
	var c Cursor
	if err := json.Unmarshal([]byte(r.Text), &c); err != nil {
		return Cursor{}, fmt.Errorf("decode cap.cursor %d: %w", r.ID, err)
	}
	c.ID = r.ID
	return c, nil
}

// CursorFrom returns the one open cursor, or nil when there is none. Rows are
// newest first; an older open cursor would be a bug, and the newest wins.
func CursorFrom(rows []Row) (*Cursor, error) {
	for _, r := range rows {
		if r.Kind != KindCursor || r.Closed {
			continue
		}
		c, err := DecodeCursor(r)
		if err != nil {
			return nil, err
		}
		return &c, nil
	}
	return nil, nil
}

// Acts returns every act row for one task, newest first.
func Acts(rows []Row, task string) ([]Act, error) {
	var out []Act
	for _, r := range rows {
		if r.Kind != KindAct || r.Closed {
			continue
		}
		a, err := DecodeAct(r)
		if err != nil {
			return nil, err
		}
		if a.Task == task {
			out = append(out, a)
		}
	}
	return out, nil
}

// OpenActs returns every open act in the rows, whatever task.
func OpenActs(rows []Row) ([]Act, error) {
	var out []Act
	for _, r := range rows {
		if r.Kind != KindAct || r.Closed {
			continue
		}
		a, err := DecodeAct(r)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// Questions returns every open question row for one task.
func Questions(rows []Row, task string) ([]Question, error) {
	var out []Question
	for _, r := range rows {
		if r.Kind != KindQuestion || r.Closed {
			continue
		}
		q, err := DecodeQuestion(r)
		if err != nil {
			return nil, err
		}
		if q.Task == task {
			out = append(out, q)
		}
	}
	return out, nil
}

// LatestAct returns the newest launched act, or nil. Ties break on row id so
// the answer is deterministic.
func LatestAct(acts []Act) *Act {
	var latest *Act
	for i := range acts {
		a := &acts[i]
		if latest == nil || a.LaunchedAt.After(latest.LaunchedAt) ||
			(a.LaunchedAt.Equal(latest.LaunchedAt) && a.ID > latest.ID) {
			latest = a
		}
	}
	return latest
}
