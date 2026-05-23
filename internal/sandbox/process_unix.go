//go:build unix

package sandbox

import (
	"os/exec"
	"syscall"
)

const processTreeCleanupAvailable = true

type processTree struct {
	cmd  *exec.Cmd
	pgid int
}

func configureProcessTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func startedProcessTree(cmd *exec.Cmd) processTree {
	tree := processTree{cmd: cmd}
	if cmd == nil || cmd.Process == nil {
		return tree
	}
	tree.pgid = cmd.Process.Pid
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		tree.pgid = pgid
	}

	return tree
}

func requestProcessTreeShutdown(tree processTree) {
	signalProcessTree(tree, syscall.SIGTERM)
}

func killProcessTree(tree processTree) {
	signalProcessTree(tree, syscall.SIGKILL)
}

func signalProcessTree(tree processTree, signal syscall.Signal) {
	if tree.cmd == nil || tree.cmd.Process == nil {
		return
	}
	if tree.pgid > 0 {
		if err := syscall.Kill(-tree.pgid, signal); err == nil || err == syscall.ESRCH {
			return
		}
	}
	_ = tree.cmd.Process.Signal(signal)
}
