package diskspace

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseBytes(t *testing.T) {
	t.Parallel()

	cases := map[string]uint64{
		"0":       0,
		"512":     512,
		"512MiB":  512 << 20,
		"1 GiB":   1 << 30,
		"1.5 GiB": 1536 << 20,
		"2GB":     2_000_000_000,
	}
	for input, expected := range cases {
		got, err := ParseBytes(input)
		if err != nil {
			t.Fatalf("expected %q to parse: %v", input, err)
		}
		if got != expected {
			t.Fatalf("expected %q to parse as %d, got %d", input, expected, got)
		}
	}
}

func TestParseBytesRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", " ", "-1GiB", "many", "12XB"} {
		if _, err := ParseBytes(input); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}

func TestCheckerRejectsLowSpaceFromInjectedStats(t *testing.T) {
	t.Parallel()

	var calls []string
	checker := NewChecker(1<<30, func(path string) (Stats, error) {
		calls = append(calls, path)
		return Stats{Path: path, AvailableBytes: 512 << 20}, nil
	})

	err := checker.Ensure("/data", "/media")
	if !errors.Is(err, ErrLowSpace) {
		t.Fatalf("expected low-space error, got %v", err)
	}
	if !strings.Contains(err.Error(), "low disk space") || !strings.Contains(err.Error(), "/data") {
		t.Fatalf("expected clear low-space error, got %v", err)
	}
	if !slices.Equal(calls, []string{"/data", "/media"}) {
		t.Fatalf("expected both configured roots to be checked, got %#v", calls)
	}
}

func TestCheckerAcceptsSufficientSpaceFromInjectedStats(t *testing.T) {
	t.Parallel()

	checker := NewChecker(1<<30, func(path string) (Stats, error) {
		return Stats{Path: path, AvailableBytes: 2 << 30}, nil
	})

	if err := checker.Ensure("/data", "/media"); err != nil {
		t.Fatalf("expected sufficient space to pass: %v", err)
	}
	report := checker.Check("/data", "/media")
	if !report.OK || len(report.Paths) != 2 {
		t.Fatalf("unexpected sufficient-space report: %#v", report)
	}
}
