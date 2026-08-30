// Package version carries the kapsel build version and release comparison
// helpers. The release build stamps Version through -ldflags
// "-X kapsel/internal/version.Version=v1.2.3"; development builds keep the
// default dev marker so update flows can refuse to act on unknown versions.
package version

import (
	"strconv"
	"strings"
)

// Version is the semantic version of this kapsel binary. Release builds
// stamp it at compile time; checkouts built without stamping report dev.
var Version = "dev"

// Dev reports whether the binary was built without a release version stamp.
// Update flows treat dev builds as ineligible for release comparisons.
func Dev() bool {
	value := strings.TrimSpace(Version)
	if value == "" || strings.EqualFold(value, "dev") {
		return true
	}

	// Development stamps like "dev-1a2b3c4" look like a plausible version
	// but are not releases; keep them ineligible for update comparisons.
	return strings.HasPrefix(strings.ToLower(value), "dev-")
}

// Compare returns -1 when left orders before right, 0 when they are equal,
// and 1 when left orders after right. Both values are optional v-prefixed
// dotted release numbers such as v1.2.3, 1.2.3, or v1.2.3-rc.1. Unparseable
// input orders before parseable input and unparseable pairs compare equal so
// callers can decide what to do when either side is unknown.
func Compare(left string, right string) int {
	leftCore, leftBuild, leftOK := splitVersion(left)
	rightCore, rightBuild, rightOK := splitVersion(right)
	if !leftOK || !rightOK {
		if leftOK {
			return 1
		}
		if rightOK {
			return -1
		}

		return 0
	}
	if comparison := compareNumericParts(leftCore, rightCore); comparison != 0 {
		return comparison
	}

	return compareBuildSuffix(leftBuild, rightBuild)
}

// Newer reports whether candidate orders strictly after current under
// Compare. Unknown versions never count as newer.
func Newer(current string, candidate string) bool {
	if !parseable(candidate) {
		return false
	}

	return Compare(current, candidate) < 0
}

func parseable(value string) bool {
	_, _, ok := splitVersion(value)

	return ok
}

func splitVersion(value string) ([]int, string, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		return nil, "", false
	}
	core := value
	build := ""
	if index := strings.IndexAny(value, "-+"); index >= 0 {
		core = value[:index]
		build = value[index+1:]
	}
	parts := strings.Split(core, ".")
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" || len(part) > 9 {
			return nil, "", false
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return nil, "", false
		}
		numbers = append(numbers, number)
	}
	if len(numbers) == 0 {
		return nil, "", false
	}

	return numbers, build, true
}

func compareNumericParts(left []int, right []int) int {
	length := len(left)
	if len(right) > length {
		length = len(right)
	}
	for index := range length {
		leftValue := 0
		if index < len(left) {
			leftValue = left[index]
		}
		rightValue := 0
		if index < len(right) {
			rightValue = right[index]
		}
		if leftValue < rightValue {
			return -1
		}
		if leftValue > rightValue {
			return 1
		}
	}

	return 0
}

func compareBuildSuffix(left string, right string) int {
	// A plain release (v1.2.3) orders after any prerelease of the same core
	// (v1.2.3-rc.1). Two suffixes compare semver-style: numeric segments
	// compare numerically (rc.10 > rc.2), numeric segments order before
	// alphanumeric ones, and with equal prefixes the suffix carrying more
	// segments orders higher (rc < rc.1).
	switch {
	case left == right:
		return 0
	case left == "":
		return 1
	case right == "":
		return -1
	}
	leftSegments := strings.Split(left, ".")
	rightSegments := strings.Split(right, ".")
	for index := 0; index < len(leftSegments) && index < len(rightSegments); index++ {
		if comparison := compareSuffixSegment(leftSegments[index], rightSegments[index]); comparison != 0 {
			return comparison
		}
	}
	if len(leftSegments) < len(rightSegments) {
		return -1
	}

	return 1
}

func compareSuffixSegment(left string, right string) int {
	leftNumber, leftErr := strconv.Atoi(left)
	rightNumber, rightErr := strconv.Atoi(right)
	switch {
	case leftErr == nil && rightErr == nil:
		if leftNumber < rightNumber {
			return -1
		}
		if leftNumber > rightNumber {
			return 1
		}

		return 0
	case leftErr == nil:
		// Numeric identifiers order before alphanumeric ones.
		return -1
	case rightErr == nil:
		return 1
	default:
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}

		return 0
	}
}
