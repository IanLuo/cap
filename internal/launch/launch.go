// Package launch starts one command detached. It knows nothing about herdr,
// panes, workers or retries: the command comes from knowledge.
package launch

import (
	"os/exec"
	"syscall"
)

// Detached starts command with sh -c in dir, in its own session, and returns
// without waiting. The child is released so it outlives cap and is not a
// zombie.
type Detached struct{}

func (Detached) Launch(dir, command string) error {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
