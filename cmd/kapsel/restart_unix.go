//go:build unix

package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// restartIntoUpdatedBinary replaces the current process image with the
// (already swapped) binary at os.Executable(). The kapsel lock is released
// when the close-on-exec lock fd closes during exec, and the new process
// re-acquires it. Run only after the HTTP server, job runner, and database
// have been shut down.
func restartIntoUpdatedBinary() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		resolved = executable
	}
	slog.Info("restarting into the updated binary", "path", resolved)

	return syscall.Exec(resolved, os.Args, os.Environ())
}
