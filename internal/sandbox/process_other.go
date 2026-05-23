//go:build !unix

package sandbox

import "os/exec"

const processTreeCleanupAvailable = false

type processTree struct {
	cmd *exec.Cmd
}

func configureProcessTree(_ *exec.Cmd) {}

func startedProcessTree(cmd *exec.Cmd) processTree {
	return processTree{cmd: cmd}
}

func requestProcessTreeShutdown(tree processTree) {
	if tree.cmd != nil && tree.cmd.Process != nil {
		_ = tree.cmd.Process.Kill()
	}
}

func killProcessTree(tree processTree) {
	if tree.cmd != nil && tree.cmd.Process != nil {
		_ = tree.cmd.Process.Kill()
	}
}
