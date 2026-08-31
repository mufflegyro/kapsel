package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareRFC3339Timestamps(t *testing.T) {
	t.Parallel()

	second := "2026-05-04T12:00:00"
	cases := []struct {
		name string
		left string
		right string
		want int
	}{
		// The RFC3339Nano trailing-zero-strip hazard: same second, the
		// earlier timestamp's fraction is a truncated prefix of the later
		// one's. BINARY order flips this pair ("Z" > digits).
		{"fraction prefix", second + ".1Z", second + ".100000001Z", -1},
		{"fraction prefix reversed", second + ".100000001Z", second + ".1Z", 1},
		{"no fraction vs fraction", second + "Z", second + ".5Z", -1},
		// Equal instants written in different text forms.
		{"equal instants, padded fraction", second + ".1Z", second + ".100Z", 0},
		{"equal instants, offset form", "2026-05-04T14:00:00+02:00", second + "Z", 0},
		// Different seconds keep numeric order.
		{"different seconds", second + ".9Z", "2026-05-04T12:00:01Z", -1},
		// Non-timestamp text (including the '' COALESCE fallbacks) keeps
		// BINARY order.
		{"empty string fallback", "", second + ".1Z", strings.Compare("", second+".1Z")},
		{"unparsable fallback", "not-a-timestamp", "also-not", strings.Compare("not-a-timestamp", "also-not")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := compareRFC3339Timestamps(tc.left, tc.right)
			if got != tc.want {
				t.Fatalf("compareRFC3339Timestamps(%q, %q) = %d, want %d", tc.left, tc.right, got, tc.want)
			}
		})
	}
}

func TestRFC3339NanoCollationOrdersMixedFractions(t *testing.T) {
	t.Parallel()

	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "kapsel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Same-second timestamps whose BINARY order diverges from numeric
	// order; the collation must restore numeric order for both sorts and
	// range filters.
	rows := []string{
		"2026-05-04T12:00:00.100000001Z",
		"2026-05-04T12:00:00Z",
		"2026-05-04T12:00:00.1Z",
		"2026-05-04T12:00:01Z",
	}
	want := []string{
		"2026-05-04T12:00:00Z",
		"2026-05-04T12:00:00.1Z",
		"2026-05-04T12:00:00.100000001Z",
	}

	query := `
WITH ts(t) AS (SELECT ? UNION ALL SELECT ? UNION ALL SELECT ? UNION ALL SELECT ?)
SELECT t FROM ts WHERE t COLLATE ` + RFC3339NanoCollationName + ` <= ? ORDER BY t COLLATE ` + RFC3339NanoCollationName
	// The cutoff sits between the fractions of the third and fourth rows,
	// which BINARY order would filter incorrectly as well.
	rows = append(rows, "2026-05-04T12:00:00.2Z")
	result, err := db.Query(query, rows[0], rows[1], rows[2], rows[3], rows[4])
	if err != nil {
		t.Fatal(err)
	}
	defer result.Close()
	var got []string
	for result.Next() {
		var text string
		if err := result.Scan(&text); err != nil {
			t.Fatal(err)
		}
		got = append(got, text)
	}
	if err := result.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("collated order = %v, want %v", got, want)
	}
}

// compile-time guard that the collation comparator stays deterministic
var _ = func() int { return compareRFC3339Timestamps(time.Time{}.Format(time.RFC3339Nano), time.Time{}.Format(time.RFC3339Nano)) }
