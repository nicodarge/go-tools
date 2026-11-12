package lockfilecheck

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nicodarge/go-tools/internal/core"
)

type value = core.Value

var (
	reject   = core.Reject
	rejectf  = core.Rejectf
	q        = core.Q
	truthy   = core.Truthy
	canonEq  = core.CanonEq
	pyEq     = core.PyEq
	sameKey  = core.SameKey
	needDict = core.NeedDict
	mget     = core.MGet
	mhas     = core.MHas
)

func str(s string) *value { return core.StrValue(s) }

func isNone(v *value) bool { return core.KindOf(v) == core.Null }

func isStr(v *value) bool { return core.IsStr(v) }

func isMap(v *value) bool { return core.IsMap(v) }

// orEmpty is v, or an empty mapping when v is empty.
func orEmpty(v *value) *value {
	if truthy(v) {
		return v
	}
	return core.MapValue(core.NewMap())
}

func dependencyMap(v *value, what string) {
	m := needDict(v, what)
	for _, name := range m.Keys {
		spec := m.Vals[name]
		if !isName(str(name)) {
			rejectf("%s names %s, which is not a package name", what, q(name))
		}
		if !isRange(spec) {
			rejectf("%s %s is %s, not a semver range", what, q(name), q(spec))
		}
	}
}

func stringList(v *value, what string) {
	if core.KindOf(v) != core.List {
		reject(what + " is not a list of strings")
	}
	for _, item := range v.L {
		if !isStr(item) {
			reject(what + " is not a list of strings")
		}
	}
}

func stringMap(v *value, what string) {
	m := needDict(v, what)
	for _, key := range m.Keys {
		if !isStr(m.Vals[key]) {
			rejectf("%s %s is not a string", what, q(key))
		}
	}
}

func plainMetadata(entry *value, what string) {
	if mhas(entry, "engines") {
		stringMap(entry.M.Get("engines"), what+" engines")
	}
	for _, key := range []string{"cpu", "os", "libc"} {
		if mhas(entry, key) {
			stringList(entry.M.Get(key), what+" "+key)
		}
	}
	if mhas(entry, "deprecated") && !isStr(entry.M.Get("deprecated")) {
		reject(what + " deprecated is not a string")
	}
}

var stampRE = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.[0-9]+)?Z\z`)

// clockSkew: the registry's clock and the runner's differ; a version up to this long after TIME
// counts as published, which only ever makes the newest version newer and the bump stricter.
const clockSkew = 300 * time.Second

// maxReleaseAge is the longest minimum release age taken, in minutes.
const maxReleaseAge = 3650 * 24 * 60

var durationRE = regexp.MustCompile(`\A([0-9]{1,7})(?: ?(minutes?|mins?|m|hours?|hrs?|h|days?|d|weeks?|w))?\z`)

// unitMinutes is the length of a duration unit, by the first letter of its name.
var unitMinutes = map[byte]int64{'m': 1, 'h': 60, 'd': 24 * 60, 'w': 7 * 24 * 60}

// parseDuration is a minimum release age: bare digits are minutes, as in pnpm's minimumReleaseAge,
// otherwise a whole number and a unit (`3 days`, `72h`, `1 week`), at most maxReleaseAge. Matching
// is on ASCII lower case, so that a non-ASCII letter never folds into a unit.
func parseDuration(text, what string) time.Duration {
	lower := []byte(text)
	for i, c := range lower {
		if c >= 0x80 {
			reject(what + ": " + q(text) + " is not a duration")
		}
		if c >= 'A' && c <= 'Z' {
			lower[i] = c + ('a' - 'A')
		}
	}
	m := durationRE.FindStringSubmatch(string(lower))
	if m == nil {
		reject(what + ": " + q(text) + " is not a duration")
	}
	unit := int64(1)
	if m[2] != "" {
		unit = unitMinutes[m[2][0]]
	}
	minutes := int64(atoi(m[1])) * unit
	if minutes > maxReleaseAge {
		reject(what + ": " + q(text) + " is longer than 3650 days")
	}
	return time.Duration(minutes) * time.Minute
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func parseStamp(x any, what string) time.Time {
	var text string
	ok := false
	switch v := x.(type) {
	case string:
		text, ok = v, true
	case *value:
		text, ok = v.S, isStr(v)
	}
	var m []string
	if ok {
		m = stampRE.FindStringSubmatch(text)
	}
	if m == nil {
		reject(what + ": " + q(x) + " is not a UTC timestamp")
	}
	year, month, day := atoi(m[1]), atoi(m[2]), atoi(m[3])
	hour, minute, second := atoi(m[4]), atoi(m[5]), atoi(m[6])
	t := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	if year < 1 || month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59 ||
		t.Day() != day || int(t.Month()) != month {
		reject(what + ": " + q(x) + " is not a timestamp")
	}
	return t
}

func lastSegment(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

func sortStrings(s []string) { sort.Strings(s) }
