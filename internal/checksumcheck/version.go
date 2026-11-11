package checksumcheck

import (
	"regexp"
	"strings"
)

// dotted reads a version of the form `[v]N(.N)*[-prerelease]`, the prerelease starting with a letter.
var dotted = regexp.MustCompile(`^v?([0-9]+(?:\.[0-9]+)*)(?:-([A-Za-z][0-9A-Za-z.-]*))?\z`)

// compareVersions is -1, 0 or 1 as a is older than, as old as, or newer than b.
func compareVersions(a, b string) int {
	ma, mb := dotted.FindStringSubmatch(a), dotted.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return compareRuns(a, b)
	}
	if c := compareCores(strings.Split(ma[1], "."), strings.Split(mb[1], ".")); c != 0 {
		return c
	}
	return comparePrerelease(ma[2], mb[2])
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

// comparePrerelease orders prerelease identifiers as semver does; no prerelease is the newest.
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
			c = strings.Compare(x, y)
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
