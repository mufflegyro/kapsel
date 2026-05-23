package diskspace

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const DefaultMinFreeBytes uint64 = 1 << 30

var ErrLowSpace = errors.New("low disk space")

var bytePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([[:alpha:]]*)$`)

var byteUnits = map[string]uint64{
	"":      1,
	"b":     1,
	"byte":  1,
	"bytes": 1,
	"k":     1_000,
	"kb":    1_000,
	"m":     1_000_000,
	"mb":    1_000_000,
	"g":     1_000_000_000,
	"gb":    1_000_000_000,
	"t":     1_000_000_000_000,
	"tb":    1_000_000_000_000,
	"ki":    1 << 10,
	"kib":   1 << 10,
	"mi":    1 << 20,
	"mib":   1 << 20,
	"gi":    1 << 30,
	"gib":   1 << 30,
	"ti":    1 << 40,
	"tib":   1 << 40,
}

type Stats struct {
	Path           string
	AvailableBytes uint64
}

type StatFunc func(path string) (Stats, error)

type Checker struct {
	minFreeBytes uint64
	stat         StatFunc
}

type Report struct {
	OK               bool         `json:"ok"`
	MinimumFreeBytes uint64       `json:"minimum_free_bytes"`
	Paths            []PathStatus `json:"paths"`
}

type PathStatus struct {
	Path             string `json:"path"`
	AvailableBytes   uint64 `json:"available_bytes"`
	MinimumFreeBytes uint64 `json:"minimum_free_bytes"`
	OK               bool   `json:"ok"`
	Warning          string `json:"warning,omitempty"`
	Error            string `json:"error,omitempty"`
}

type LowSpaceError struct {
	Path             string
	AvailableBytes   uint64
	MinimumFreeBytes uint64
}

func (e LowSpaceError) Error() string {
	return lowSpaceWarning(e.Path, e.AvailableBytes, e.MinimumFreeBytes)
}

func (e LowSpaceError) Is(target error) bool {
	return target == ErrLowSpace
}

func NewChecker(minFreeBytes uint64, stat StatFunc) Checker {
	if stat == nil {
		stat = Stat
	}

	return Checker{minFreeBytes: minFreeBytes, stat: stat}
}

func (c Checker) Ensure(paths ...string) error {
	if c.minFreeBytes == 0 {
		return nil
	}

	return c.Check(paths...).Err()
}

func (c Checker) Check(paths ...string) Report {
	if c.stat == nil {
		c.stat = Stat
	}
	report := Report{OK: true, MinimumFreeBytes: c.minFreeBytes}
	for _, path := range uniqueCleanPaths(paths) {
		status := PathStatus{Path: path, MinimumFreeBytes: c.minFreeBytes, OK: true}
		stats, err := c.stat(path)
		if stats.Path != "" {
			status.Path = stats.Path
		}
		status.AvailableBytes = stats.AvailableBytes
		if err != nil {
			status.OK = false
			status.Error = err.Error()
			report.OK = false
		} else if c.minFreeBytes > 0 && status.AvailableBytes < c.minFreeBytes {
			status.OK = false
			status.Warning = lowSpaceWarning(status.Path, status.AvailableBytes, c.minFreeBytes)
			report.OK = false
		}
		report.Paths = append(report.Paths, status)
	}

	return report
}

func (r Report) Err() error {
	for _, status := range r.Paths {
		if status.Error != "" {
			return fmt.Errorf("disk space check failed for %q: %s", status.Path, status.Error)
		}
		if !status.OK {
			return LowSpaceError{Path: status.Path, AvailableBytes: status.AvailableBytes, MinimumFreeBytes: status.MinimumFreeBytes}
		}
	}

	return nil
}

func ParseBytes(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("byte value is required")
	}
	matches := bytePattern.FindStringSubmatch(value)
	if matches == nil {
		return 0, fmt.Errorf("invalid byte value %q", value)
	}
	number, err := strconv.ParseFloat(matches[1], 64)
	if err != nil || number < 0 {
		return 0, fmt.Errorf("invalid byte value %q", value)
	}
	multiplier, ok := byteUnits[strings.ToLower(matches[2])]
	if !ok {
		return 0, fmt.Errorf("unsupported byte unit %q", matches[2])
	}
	bytes := number * float64(multiplier)
	if math.IsInf(bytes, 0) || math.IsNaN(bytes) || bytes > float64(math.MaxUint64) {
		return 0, fmt.Errorf("byte value %q is too large", value)
	}

	return uint64(bytes), nil
}

func FormatBytes(bytes uint64) string {
	units := []struct {
		name string
		size uint64
	}{
		{name: "TiB", size: 1 << 40},
		{name: "GiB", size: 1 << 30},
		{name: "MiB", size: 1 << 20},
		{name: "KiB", size: 1 << 10},
	}
	for _, unit := range units {
		if bytes >= unit.size {
			value := float64(bytes) / float64(unit.size)
			if bytes%unit.size == 0 {
				return fmt.Sprintf("%.0f %s", value, unit.name)
			}

			return fmt.Sprintf("%.1f %s", value, unit.name)
		}
	}

	return fmt.Sprintf("%d B", bytes)
}

func uniqueCleanPaths(paths []string) []string {
	seen := map[string]bool{}
	var cleaned []string
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		cleaned = append(cleaned, path)
	}

	return cleaned
}

func lowSpaceWarning(path string, availableBytes uint64, minimumFreeBytes uint64) string {
	return fmt.Sprintf("low disk space for %q: %s available, minimum %s configured", path, FormatBytes(availableBytes), FormatBytes(minimumFreeBytes))
}
