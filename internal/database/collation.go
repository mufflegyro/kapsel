package database

import (
	"strings"
	"time"

	"modernc.org/sqlite"
)

// RFC3339NanoCollationName orders RFC 3339 timestamp text (the mixed formats
// stored in jobs, videos, and user_progress: Go's RFC3339Nano with trailing
// zeros stripped, and SQLite's strftime('%Y-%m-%dT%H:%M:%fZ', 'now') with a
// fixed 3-digit fraction). BINARY order diverges from numeric order when two
// timestamps fall in the same second and the earlier one's printed fraction is
// a truncated prefix of the later one's ("…00.1Z" sorts above
// "…00.100000001Z" because 'Z' > digits), so timestamp comparisons and sorts
// on these columns must opt in via COLLATE RFC3339_NANO. Comparison-time
// normalization only: no stored data is rewritten.
const RFC3339NanoCollationName = "RFC3339_NANO"

// Registering at package init makes the collation available to every
// connection opened after the import, including database/sql pools opened
// lazily by Open. modernc.org/sqlite keeps driver-level registrations for the
// lifetime of the process.
func init() {
	sqlite.MustRegisterCollationUtf8(RFC3339NanoCollationName, compareRFC3339Timestamps)
}

// compareRFC3339Timestamps compares two text timestamps numerically. Text
// that does not parse as an RFC 3339 timestamp (including the '' fallbacks
// produced by COALESCE(NULLIF(...), ...)) falls back to BINARY order so
// existing sort behavior for non-timestamp values is preserved.
func compareRFC3339Timestamps(left, right string) int {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	if leftErr != nil || rightErr != nil {
		return strings.Compare(left, right)
	}
	return leftTime.Compare(rightTime)
}
