//go:build !linux

package bootstrapapply

import (
	"os/exec"
	"time"
)

// Real bootstrap execution is Linux-only. Other platforms compile the fake
// runner and command-shape tests without Unix process-group assumptions.
func configureProcess(command *exec.Cmd) { command.WaitDelay = 10 * time.Second }
