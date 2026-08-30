package version

import "testing"

func TestCompareOrdersVersions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		left     string
		right    string
		expected int
	}{
		{"v1.2.3", "v1.2.4", -1},
		{"1.2.3", "v1.2.3", 0},
		{"v1.10.0", "v1.9.9", 1},
		{"v2.0.0", "v1.99.99", 1},
		{"v1.2.3", "v1.2", 1},
		// Semver equality: 1.2 and 1.2.0 are the same release. A same-release
		// re-tag then reconciles instead of re-swapping the binary.
		{"v1.2", "v1.2.0", 0},
		{"v1.2.3-rc.1", "v1.2.3", -1},
		{"v1.2.3", "v1.2.4-rc.1", -1},
		{"v1.2.4-rc.1", "v1.2.3", 1},
		// Numeric prerelease segments compare numerically, not lexically.
		{"v1.2.3-rc.2", "v1.2.3-rc.10", -1},
		{"v1.2.3-rc.10", "v1.2.3-rc.2", 1},
		// Equal prefixes: more segments order higher; numeric segments order
		// before alphanumeric ones.
		{"v1.2.3-rc", "v1.2.3-rc.1", -1},
		{"v1.2.3-1", "v1.2.3-alpha", -1},
		{"v1.2.3-alpha", "v1.2.3-beta", -1},
		{"dev", "v1.0.0", -1},
		{"v1.0.0", "dev", 1},
		{"garbage", "also garbage", 0},
	}

	for _, test := range cases {
		if got := Compare(test.left, test.right); got != test.expected {
			t.Errorf("Compare(%q, %q) = %d, want %d", test.left, test.right, got, test.expected)
		}
	}
}

func TestNewer(t *testing.T) {
	t.Parallel()

	if Newer("v1.2.3", "v1.2.4") != true {
		t.Error("v1.2.4 should be newer than v1.2.3")
	}
	if Newer("v1.2.3", "v1.2.3") != false {
		t.Error("equal versions are not newer")
	}
	if Newer("v1.2.4", "v1.2.3") != false {
		t.Error("older candidate is not newer")
	}
	if Newer("v1.2.3", "nonsense") != false {
		t.Error("unparseable candidate never counts as newer")
	}
	if Newer("nonsense", "v1.2.4") != true {
		t.Error("unknown current yields newer candidate")
	}
	if Newer("v1.2.3", "") != false {
		t.Error("empty candidate is not newer")
	}
}

func TestDevDetectsUnstampedBuilds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value    string
		expected bool
	}{
		{"dev", true},
		{"", true},
		{"DEV", true},
		{"dev-1a2b3c4", true},
		{"dev-anything", true},
		{"v1.2.3", false},
		{"0.1.0", false},
	}

	original := Version
	defer func() { Version = original }()
	for _, test := range tests {
		Version = test.value
		if got := Dev(); got != test.expected {
			t.Errorf("Dev() with Version=%q = %v, want %v", test.value, got, test.expected)
		}
	}
}
