//go:build !unix

package main

import "errors"

// restartIntoUpdatedBinary is unavailable outside unix platforms. The caller
// logs the error and exits non-zero so a supervisor restarts the service into
// the swapped binary.
func restartIntoUpdatedBinary() error {
	return errors.New("in-process restart is only supported on unix platforms; start the service again to run the updated binary")
}
