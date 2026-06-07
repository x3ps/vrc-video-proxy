//go:build !windows

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func stopProcess(process *os.Process, logger *slog.Logger) {
	if process == nil {
		return
	}

	logger.Info("stopping process", "pid", process.Pid)

	pgid, err := syscall.Getpgid(process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		logger.Warn("failed to find process group, signaling process only", "error", err)
		_ = process.Signal(syscall.SIGTERM)
	}

	time.Sleep(3 * time.Second)
	if err := process.Signal(syscall.Signal(0)); err == nil {
		if pgid, pgidErr := syscall.Getpgid(process.Pid); pgidErr == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = process.Kill()
		}
	}
}
