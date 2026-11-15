// Package agents is cap's view of the agent harness: one read of the panes and
// the three bounded actions tier 1 takes. It never reads a pane's text, a
// transcript, or a diff; the harness is a status source, not a worker API.
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Pane is one harness pane. Agent is empty when no agent was detected; Status
// is working, idle, blocked, done, gone, or unknown.
type Pane struct {
	ID     string
	Agent  string
	Status string
	CWD    string
}

// Harness is the one seam tier 1 speaks through. List is a read; the other
// three are the only side effects cap takes without an instruction.
type Harness interface {
	List(ctx context.Context) ([]Pane, error)
	Prompt(ctx context.Context, target, text string) error
	Close(ctx context.Context, pane string) error
	Notify(ctx context.Context, title, body string) error
}

// Herdr runs the herdr CLI.
type Herdr struct {
	Bin string // defaults to "herdr"
}

func (h Herdr) bin() string {
	if h.Bin == "" {
		return "herdr"
	}
	return h.Bin
}

func (h Herdr) List(ctx context.Context) ([]Pane, error) {
	out, err := exec.CommandContext(ctx, h.bin(), "pane", "list").Output()
	if err != nil {
		return nil, runError(h.bin(), err, "pane list")
	}
	return DecodePanes(out)
}

func (h Herdr) Prompt(ctx context.Context, target, text string) error {
	return run(ctx, h.bin(), "agent", "prompt", target, text)
}

func (h Herdr) Close(ctx context.Context, pane string) error {
	return run(ctx, h.bin(), "pane", "close", pane)
}

func (h Herdr) Notify(ctx context.Context, title, body string) error {
	return run(ctx, h.bin(), "notification", "show", title, "--body", body)
}

func run(ctx context.Context, bin string, args ...string) error {
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return runError(bin, err, strings.Join(args, " "))
	}
	return decodeEnvelope(out)
}

func runError(bin string, err error, what string) error {
	if ee, ok := err.(*exec.ExitError); ok {
		msg := strings.TrimSpace(string(ee.Stderr))
		if msg != "" {
			return fmt.Errorf("%s %s: %w: %s", bin, what, err, msg)
		}
	}
	return fmt.Errorf("%s %s: %w", bin, what, err)
}

type envelope struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func decodeEnvelope(out []byte) error {
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("decode herdr answer: %w", err)
	}
	if env.Error != nil {
		return fmt.Errorf("herdr refused: %s", env.Error.Message)
	}
	return nil
}

type paneEnvelope struct {
	Result struct {
		Panes []paneJSON `json:"panes"`
	} `json:"result"`
}

type paneJSON struct {
	ID     string `json:"pane_id"`
	Agent  string `json:"agent"`
	Status string `json:"agent_status"`
	CWD    string `json:"cwd"`
}

// DecodePanes reads herdr's pane list. A pane with no agent carries no agent
// field; that is the class empty panes are closed on.
func DecodePanes(out []byte) ([]Pane, error) {
	var env paneEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("decode herdr pane list: %w", err)
	}
	panes := make([]Pane, 0, len(env.Result.Panes))
	for _, p := range env.Result.Panes {
		panes = append(panes, Pane{ID: p.ID, Agent: p.Agent, Status: p.Status, CWD: p.CWD})
	}
	return panes, nil
}
