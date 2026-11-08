// Package service controls cap's own watcher process through launchd. It knows
// one label: cap's. It never starts a task command and never reads a worker.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultLabel is the launchd label the nix module installs.
const DefaultLabel = "home-manager.cap"

// State is what launchd answers about the label.
type State struct {
	Loaded bool
	PID    int
}

// Controller controls one supervised process.
type Controller interface {
	State(ctx context.Context) (State, error)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Launchd controls one launchd agent in the user's gui domain.
type Launchd struct {
	Bin   string
	Label string
	Plist string
	UID   int
}

// New reads the label and binary from the environment, with the same defaults
// the nix module uses.
func New() Launchd {
	label := DefaultLabel
	if v := os.Getenv("CAP_SERVICE_LABEL"); v != "" {
		label = v
	}
	bin := "launchctl"
	if v := os.Getenv("CAP_LAUNCHCTL"); v != "" {
		bin = v
	}
	plist := os.Getenv("CAP_SERVICE_PLIST")
	if plist == "" {
		if home, err := os.UserHomeDir(); err == nil {
			plist = filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		}
	}
	return Launchd{Bin: bin, Label: label, Plist: plist, UID: os.Getuid()}
}

func (l Launchd) domain() string { return "gui/" + strconv.Itoa(l.UID) }
func (l Launchd) target() string { return l.domain() + "/" + l.Label }

// State asks launchd about the label. Exit 113 is launchd's "no such service":
// that is an answer (stopped), not a failure to answer.
func (l Launchd) State(ctx context.Context) (State, error) {
	out, errText, err := l.run(ctx, "print", l.target())
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 113 {
			return State{}, nil
		}
		return State{}, fmt.Errorf("launchctl print %s: %w: %s", l.target(), err, strings.TrimSpace(errText))
	}
	return State{Loaded: true, PID: parsePID(out)}, nil
}

// Start brings the watcher up. A running watcher is left alone; a loaded agent
// without a pid is kicked; an unloaded agent is bootstrapped from its plist.
func (l Launchd) Start(ctx context.Context) error {
	st, err := l.State(ctx)
	if err != nil {
		return err
	}
	switch {
	case st.Loaded && st.PID > 0:
		return nil
	case st.Loaded:
		_, errText, err := l.run(ctx, "kickstart", "-k", l.target())
		if err != nil {
			return fmt.Errorf("launchctl kickstart %s: %w: %s", l.target(), err, strings.TrimSpace(errText))
		}
		return nil
	}
	if l.Plist == "" {
		return errors.New("no service plist path: set CAP_SERVICE_PLIST")
	}
	if _, err := os.Stat(l.Plist); err != nil {
		return fmt.Errorf("service plist %s: %w", l.Plist, err)
	}
	_, errText, err := l.run(ctx, "bootstrap", l.domain(), l.Plist)
	if err != nil {
		return fmt.Errorf("launchctl bootstrap %s %s: %w: %s", l.domain(), l.Plist, err, strings.TrimSpace(errText))
	}
	return nil
}

// Stop unloads the agent. An unloaded agent is already stopped.
func (l Launchd) Stop(ctx context.Context) error {
	st, err := l.State(ctx)
	if err != nil {
		return err
	}
	if !st.Loaded {
		return nil
	}
	_, errText, err := l.run(ctx, "bootout", l.target())
	if err != nil {
		return fmt.Errorf("launchctl bootout %s: %w: %s", l.target(), err, strings.TrimSpace(errText))
	}
	return nil
}

func (l Launchd) run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, l.Bin, args...)
	var out, errText bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errText
	err = cmd.Run()
	return out.String(), errText.String(), err
}

// parsePID reads the pid line from `launchctl print`. A loaded agent that is
// waiting to restart has no pid: that is 0, not an error.
func parsePID(out string) int {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "pid = "); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err == nil {
				return n
			}
		}
	}
	return 0
}
