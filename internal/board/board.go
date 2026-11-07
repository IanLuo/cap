// Package board is cap's only view of fs: one `fs status` read per tick. It
// never claims, reports or writes a task, and it never reads a pane, a diff or
// a transcript.
package board

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Task is one fs task as `fs status` reports it, with the project it belongs to
// filled in from the enclosing project entry. The order of tasks is the order
// fs returns: oldest first within a project.
type Task struct {
	Task         string     `json:"task"`
	Project      string     `json:"-"`
	Kind         string     `json:"kind"`
	State        string     `json:"state"`
	Goal         string     `json:"goal"`
	Checks       []string   `json:"checks"`
	ProjectPath  string     `json:"project_path,omitempty"`
	ParentDev    string     `json:"parent_dev,omitempty"`
	Session      string     `json:"session,omitempty"`
	Round        int        `json:"round,omitempty"`
	LeaseExpires *time.Time `json:"lease_expires,omitempty"`
	LeaseLive    bool       `json:"lease_live"`
	Agent        Observed   `json:"agent"`
	Runtime      Observed   `json:"runtime"`
	Evidence     []Evidence `json:"evidence,omitempty"`
}

// Observed is what fs's status says an agent or runtime last reported.
type Observed struct {
	State    string     `json:"state"`
	Reported string     `json:"reported,omitempty"`
	Session  string     `json:"session,omitempty"`
	Role     string     `json:"role,omitempty"`
	Source   string     `json:"source"`
	At       *time.Time `json:"at,omitempty"`
	Age      *float64   `json:"age_seconds,omitempty"`
	Fresh    bool       `json:"fresh"`
}

// Evidence is one fs report on a task.
type Evidence struct {
	State   string     `json:"state"`
	Role    string     `json:"role,omitempty"`
	Note    string     `json:"note,omitempty"`
	Session string     `json:"session,omitempty"`
	At      *time.Time `json:"at,omitempty"`
	Age     *float64   `json:"age_seconds,omitempty"`
}

// Reader is the board seam. The watcher and dispatcher depend on this one
// method, so a test can count reads and a fake can never be asked to write.
type Reader interface {
	Status(context.Context) ([]Task, error)
}

// Client runs exactly one `fs status` over every project.
type Client struct {
	Bin string // defaults to "fs"
}

func (c Client) Status(ctx context.Context) ([]Task, error) {
	bin := c.Bin
	if bin == "" {
		bin = "fs"
	}
	cmd := exec.CommandContext(ctx, bin, "status", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			msg := strings.TrimSpace(string(ee.Stderr))
			if msg != "" {
				return nil, fmt.Errorf("%s status: %w: %s", bin, err, msg)
			}
		}
		return nil, fmt.Errorf("%s status: %w", bin, err)
	}
	return Decode(out)
}

type envelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  struct {
		Projects []struct {
			Project string `json:"project"`
			Tasks   []Task `json:"tasks"`
		} `json:"projects"`
	} `json:"data"`
}

// Decode reads fs's JSON envelope and flattens the projects into one task list.
// A refusal or a malformed answer is an error: an unreadable board fails the
// tick closed.
func Decode(out []byte) ([]Task, error) {
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("decode fs status: %w", err)
	}
	if !env.OK {
		if env.Error != "" {
			return nil, fmt.Errorf("fs status refused: %s", env.Error)
		}
		return nil, fmt.Errorf("fs status returned no ok envelope")
	}
	tasks := make([]Task, 0)
	for _, p := range env.Data.Projects {
		for _, t := range p.Tasks {
			t.Project = p.Project
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}
