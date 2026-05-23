//go:build !unix

package diskspace

import "errors"

func Stat(path string) (Stats, error) {
	return Stats{Path: path}, errors.New("disk space checks are unsupported on this platform")
}
