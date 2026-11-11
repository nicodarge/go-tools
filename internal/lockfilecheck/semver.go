package lockfilecheck

import (
	"regexp"
	"strings"

	"github.com/nicodarge/go-tools/internal/core"
)

// Semver ranges, read as node-semver reads them, for the grammar of rangeRE only.

const (
	nameSrc   = `(?:@[A-Za-z0-9~][A-Za-z0-9._~-]*/)?[A-Za-z0-9~][A-Za-z0-9._~-]*`
	semverSrc = `(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?`
	buildSrc  = `(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?`

	// rangeAtom is one comparator of a range. The separator between atoms is required: an optional
	// one lets two atoms split a digit run any way they like.
	rangeAtom = `(?:\*|[xX]|(?:[~^=]|[<>]=?)?v?[0-9]+(?:\.(?:[0-9]+|[xX*])){0,2}` +
		`(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?` +
		`(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)`
)

var (
	nameRE            = regexp.MustCompile(`^` + nameSrc + `\z`)
	npmVersionRE      = regexp.MustCompile(`^` + semverSrc + `\z`)
	pnpmVersionRE     = regexp.MustCompile(`^` + semverSrc + buildSrc)
	pnpmVersionFullRE = regexp.MustCompile(`^` + semverSrc + buildSrc + `\z`)
	pnpmNameAtRE      = regexp.MustCompile(`^(` + nameSrc + `)@`)
	integrityRE       = regexp.MustCompile(`^sha(?:1|256|384|512)-[A-Za-z0-9+/]+={0,2}\z`)
	npmKeyRE          = regexp.MustCompile(`^(?:node_modules/` + nameSrc + `/)*node_modules/(` + nameSrc + `)\z`)
	rangeRE           = regexp.MustCompile(`^` + rangeAtom + `(?:(?:[ \t]+|[ \t]*\|\|[ \t]*)` + rangeAtom + `)*\z`)
	fileSpecRE        = regexp.MustCompile(`(?i)\.(?:tgz|tar|tar\.gz)\z`)
	versionRE         = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z.-]+)?\z`)
	atomRE            = regexp.MustCompile(`^([~^=]|[<>]=?)?v?([0-9]+)(?:\.([0-9]+|[xX*]))?(?:\.([0-9]+|[xX*]))?` +
		`(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z.-]+)?\z`)
)

func isRange(v *core.Value) bool {
	return core.IsStr(v) && len(v.S) <= 256 && rangeRE.MatchString(v.S) && !fileSpecRE.MatchString(v.S)
}

func isName(v *core.Value) bool { return core.IsStr(v) && nameRE.MatchString(v.S) }

func isIntegrity(v *core.Value) bool { return core.IsStr(v) && integrityRE.MatchString(v.S) }

// A number of any size, as its decimal digits without leading zeros.
func normNum(s string) string {
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0"
	}
	return s
}

func cmpNum(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func incNum(s string) string {
	b := []byte(s)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != '9' {
			b[i]++
			return string(b)
		}
		b[i] = '0'
	}
	return "1" + string(b)
}

type version struct {
	major, minor, patch string
	pre                 []string
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func cmpPreID(a, b string) int {
	da, db := isDigits(a), isDigits(b)
	switch {
	case da && db:
		return cmpNum(normNum(a), normNum(b))
	case da:
		return -1
	case db:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpVersion(a, b version) int {
	if c := cmpNum(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpNum(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmpNum(a.patch, b.patch); c != 0 {
		return c
	}
	ap, bp := len(a.pre) > 0, len(b.pre) > 0
	if ap != bp {
		if ap {
			return -1
		}
		return 1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := cmpPreID(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1
	case len(a.pre) > len(b.pre):
		return 1
	}
	return 0
}

func prerelease(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, ".")
}

func parseVersion(x any) version {
	var text string
	switch v := x.(type) {
	case string:
		text = v
	case *core.Value:
		if !core.IsStr(v) {
			core.Reject(core.Q(v) + " is not a version")
		}
		text = v.S
	}
	m := versionRE.FindStringSubmatch(text)
	if m == nil {
		core.Reject(core.Q(text) + " is not a version")
	}
	return version{normNum(m[1]), normNum(m[2]), normNum(m[3]), prerelease(m[4])}
}

type comparator struct {
	op    string
	bound version
}

func comparators(atom string) []comparator {
	zero := version{"0", "0", "0", nil}
	if atom == "*" || atom == "x" || atom == "X" {
		return []comparator{{">=", zero}}
	}
	m := atomRE.FindStringSubmatch(atom)
	if m == nil {
		core.Reject("the range " + core.Q(atom) + " cannot be evaluated")
	}
	op := m[1]
	if op == "=" {
		op = ""
	}
	wild := func(s string) bool { return s == "" || s == "x" || s == "X" || s == "*" }
	major := normNum(m[2])
	hasMinor := !wild(m[3])
	hasPatch := hasMinor && !wild(m[4])
	minor, patch := "0", "0"
	if hasMinor {
		minor = normNum(m[3])
	}
	if hasPatch {
		patch = normNum(m[4])
	}
	pre := prerelease(m[5])
	if len(pre) > 0 && !hasPatch {
		core.Reject("the range " + core.Q(atom) + " cannot be evaluated")
	}
	low := version{major, minor, patch, pre}
	zeroPre := []string{"0"}
	belowNextMajor := comparator{"<", version{incNum(major), "0", "0", zeroPre}}
	belowNextMinor := comparator{"<", version{major, incNum(minor), "0", zeroPre}}
	ge := comparator{">=", low}
	switch {
	case op == "" && !hasMinor:
		return []comparator{ge, belowNextMajor}
	case op == "" && !hasPatch:
		return []comparator{ge, belowNextMinor}
	case op == "":
		return []comparator{{"=", low}}
	case op == "~":
		if !hasMinor {
			return []comparator{ge, belowNextMajor}
		}
		return []comparator{ge, belowNextMinor}
	case op == "^":
		switch {
		case !hasMinor:
			return []comparator{ge, belowNextMajor}
		case !hasPatch:
			if major == "0" {
				return []comparator{ge, belowNextMinor}
			}
			return []comparator{ge, belowNextMajor}
		case major != "0":
			return []comparator{ge, belowNextMajor}
		case minor != "0":
			return []comparator{ge, belowNextMinor}
		}
		return []comparator{ge, {"<", version{"0", "0", incNum(patch), zeroPre}}}
	case op == ">":
		switch {
		case !hasMinor:
			return []comparator{{">=", version{incNum(major), "0", "0", nil}}}
		case !hasPatch:
			return []comparator{{">=", version{major, incNum(minor), "0", nil}}}
		}
		return []comparator{{">", low}}
	case op == ">=":
		return []comparator{ge}
	case op == "<":
		if !hasPatch || !hasMinor {
			p := pre
			if len(p) == 0 {
				p = zeroPre
			}
			return []comparator{{"<", version{low.major, low.minor, low.patch, p}}}
		}
		return []comparator{{"<", low}}
	}
	// "<="
	switch {
	case !hasMinor:
		return []comparator{{"<", version{incNum(major), "0", "0", zeroPre}}}
	case !hasPatch:
		return []comparator{{"<", version{major, incNum(minor), "0", zeroPre}}}
	}
	return []comparator{{"<=", low}}
}

func holds(op string, v, bound version) bool {
	c := cmpVersion(v, bound)
	switch op {
	case "=":
		return c == 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	case "<":
		return c < 0
	}
	return c <= 0
}

// satisfies: whether a version is in a range of the grammar of rangeRE. A range outside that
// grammar cannot be evaluated and is a refusal, not a pass.
func satisfies(versionText any, rangeText *core.Value) bool {
	if !isRange(rangeText) {
		core.Reject("the range " + core.Q(rangeText) + " cannot be evaluated")
	}
	v := parseVersion(versionText)
	for _, alternative := range strings.Split(rangeText.S, "||") {
		var parsed []comparator
		for _, atom := range strings.Fields(alternative) {
			parsed = append(parsed, comparators(atom)...)
		}
		all := true
		for _, c := range parsed {
			if !holds(c.op, v, c.bound) {
				all = false
				break
			}
		}
		if !all {
			continue
		}
		// A prerelease satisfies a range only through a comparator naming a prerelease of the
		// very same major.minor.patch.
		if len(v.pre) == 0 {
			return true
		}
		for _, c := range parsed {
			if len(c.bound.pre) > 0 && c.bound.major == v.major && c.bound.minor == v.minor && c.bound.patch == v.patch {
				return true
			}
		}
	}
	return false
}

func satisfiesStr(versionText, rangeText string) bool {
	return satisfies(versionText, core.StrValue(rangeText))
}

// notLower: an older version that is still in range is a downgrade too.
func notLower(versionText, beforeText any, what string) {
	if cmpVersion(parseVersion(versionText), parseVersion(beforeText)) < 0 {
		core.Reject(what + ": " + core.Q(versionText) + " is lower than " + core.Q(beforeText) + ", which the base had")
	}
}

func mustSatisfy(versionText any, rangeText *core.Value, what string) {
	if !satisfies(versionText, rangeText) {
		core.Reject(what + ": " + core.Q(versionText) + " is not in the range " + core.Q(rangeText))
	}
}
