package checksumcheck

import (
	"regexp"
	"strings"
)

// dottedVersion reads the dotted number a version starts with (an optional `v` before it) and
// what follows it.
var dottedVersion = regexp.MustCompile(`^v?([0-9]+(?:\.[0-9]+)*)(.*)\z`)

// splitVersion reads a version as a dotted number and a prerelease, so that 1.2.3 is newer than
// every prerelease of 1.2.3 however it is spelled: `-rc.1` and `-1` (semver), `~rc1`, `_rc1`,
// `.rc1`, and a run of letters glued to the number (`rc1`). Build metadata after `+` is dropped.
// ok is false for anything else, which is compared run by run.
func splitVersion(version string) (parts []string, pre string, ok bool) {
	if i := strings.IndexByte(version, '+'); i >= 0 {
		version = version[:i]
	}
	m := dottedVersion.FindStringSubmatch(version)
	if m == nil {
		return nil, "", false
	}
	parts, rest := strings.Split(m[1], "."), m[2]
	switch {
	case rest == "":
		return parts, "", true
	case rest[0] == '-' || rest[0] == '_' || rest[0] == '~':
		return parts, rest[1:], rest[1:] != ""
	case rest[0] == '.' && len(rest) > 1 && isLetter(rest[1]):
		return parts, rest[1:], true
	case isLetter(rest[0]):
		return parts, rest, true
	}
	return nil, "", false
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// compareVersions is -1, 0 or 1 as a is older than, as old as, or newer than b.
func compareVersions(a, b string) int {
	ca, pa, oka := splitVersion(a)
	cb, pb, okb := splitVersion(b)
	if !oka || !okb {
		return compareRuns(a, b)
	}
	if c := compareCores(ca, cb); c != 0 {
		return c
	}
	return comparePrerelease(pa, pb)
}

// compareNumbers compares two strings of digits by value, of any length.
func compareNumbers(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return cmpInt(len(a), len(b))
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compareCores compares dotted numbers, a missing part counting as 0.
func compareCores(a, b []string) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := "0", "0"
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if c := compareNumbers(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func isNumber(s string) bool { return strings.Trim(s, "0123456789") == "" }

// comparePrerelease orders prerelease identifiers as semver does, a numeric one before an
// alphanumeric one; no prerelease is the newest.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	ia, ib := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ia) && i < len(ib); i++ {
		x, y := ia[i], ib[i]
		var c int
		switch {
		case isNumber(x) && isNumber(y):
			c = compareNumbers(x, y)
		case isNumber(x):
			c = -1
		case isNumber(y):
			c = 1
		default:
			c = compareRuns(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return cmpInt(len(ia), len(ib))
}

// runs splits s into maximal runs of digits and of other characters.
func runs(s string) []string {
	var out []string
	start := 0
	for i := 1; i <= len(s); i++ {
		if i == len(s) || (s[i] >= '0' && s[i] <= '9') != (s[i-1] >= '0' && s[i-1] <= '9') {
			out = append(out, s[start:i])
			start = i
		}
	}
	return out
}

// compareRuns compares two tokens run by run: digit runs by value, other runs bytewise, and the
// shorter token is the older when one is a prefix of the other.
func compareRuns(a, b string) int {
	ra, rb := runs(a), runs(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		x, y := ra[i], rb[i]
		var c int
		switch dx, dy := isNumber(x), isNumber(y); {
		case dx && dy:
			c = compareNumbers(x, y)
		case dx:
			c = 1
		case dy:
			c = -1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return cmpInt(len(ra), len(rb))
}
