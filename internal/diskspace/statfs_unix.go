//go:build unix

package diskspace

import "golang.org/x/sys/unix"

func Stat(path string) (Stats, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return Stats{Path: path}, err
	}

	return Stats{Path: path, AvailableBytes: stat.Bavail * uint64(stat.Bsize)}, nil
}
