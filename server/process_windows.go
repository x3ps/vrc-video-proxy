//go:build windows

package main

import (
	"log/slog"
	"os"
	"os/exec"
)

func configureCommand(_ *exec.Cmd) {}

func stopProcess(process *os.Process, logger *slog.Logger) {
	if process == nil {
		return
	}

	logger.Info("stopping process", "pid", process.Pid)
	if err := process.Signal(os.Interrupt); err != nil {
		_ = process.Kill()
	}
}
