package lockfilecheck

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/go-tools/internal/core"
)

const (
	ageNow  = "2026-11-12T12:00:00Z"
	dayOld  = "2026-11-11T12:00:00.000Z"
	refused = "neither the newest"
)

// ageIntegrity is the integrity of each version of "zod" the pnpm cases publish: 4.6.5 is held by the base, 4.6.6
// and 4.6.7 are old, 4.6.8 is the newest and published at the time the case gives.
var ageIntegrity = map[string]string{"4.6.5": intA, "4.6.6": intB, "4.6.7": intC, "4.6.8": intD}

func agePNPMRegistry(newestStamp string) registry {
	times := object{"4.6.5": longAgo, "4.6.6": longAgo, "4.6.7": longAgo, "4.6.8": newestStamp}
	versions := object{"4.6.5": object{}, "4.6.6": object{}, "4.6.7": object{}, "4.6.8": object{}}
	manifests := map[string]object{}
	for v, integrity := range ageIntegrity {
		manifests["zod@"+v] = manifestOf("zod", v, integrity, nil)
	}
	return registry{manifests: manifests,
		packuments: map[string]object{"zod": {"name": "zod", "time": times, "versions": versions}}}
}

// ageHead is the pnpm base with zod moved to version, integrity included.
func agePNPMBase() string {
	return strings.NewReplacer("INT_A", intA, "INT_C", intC).Replace(pnpmBase)
}

func ageHead(version string) string {
	text := agePNPMBase()
	text = strings.ReplaceAll(text, "zod@4.6.5:\n    resolution: {integrity: "+intA+"}", "zod@"+version+":\n    resolution: {integrity: "+ageIntegrity[version]+"}")
	text = strings.ReplaceAll(text, "zod@4.6.5: {}", "zod@"+version+": {}")
	return strings.ReplaceAll(text, "version: 4.6.5", "version: "+version)
}

type ageCase struct {
	label string
	head  string
	stamp string // when 4.6.8 was published
	age   string // "" for no --min-release-age
	want  string // "" for PASS, else a substring of the refusal
}

func runAge(t *testing.T, path, base, head string, reg registry, age string) (int, string) {
	t.Helper()
	options := []string{"--before", ageNow}
	if age != "" {
		options = append(options, "--min-release-age", age)
	}
	var out bytes.Buffer
	code := runMainWith(t, &out, path, base, head, reg, nil, options...)
	first, _, _ := strings.Cut(out.String(), "\n")
	return code, first
}

func TestMinReleaseAgePNPM(t *testing.T) {
	base := agePNPMBase()
	if ageHead("4.6.5") != base {
		t.Fatal("the head builder must give the base back for the held version")
	}
	cases := []ageCase{
		{"the newest is a day old and the age 3 days, so the one before it is the newest", ageHead("4.6.7"), dayOld, "3 days", ""},
		{"the same lockfile without an age is refused", ageHead("4.6.7"), dayOld, "", refused},
		{"the same lockfile with an age of 0 is refused", ageHead("4.6.7"), dayOld, "0", refused},
		{"4320 minutes are 3 days", ageHead("4.6.7"), dayOld, "4320", ""},
		{"72 hours are 3 days", ageHead("4.6.7"), dayOld, "72 hours", ""},
		{"an age shorter than the newest version is old is refused", ageHead("4.6.7"), dayOld, "12 hours", refused},
		{"a version older than the newest one old enough is refused", ageHead("4.6.6"), dayOld, "3 days", refused},
		{"the newest, younger than the age and not held by the base, is refused", ageHead("4.6.8"), dayOld, "3 days", refused},
		{"a newest version old enough is the newest", ageHead("4.6.8"), "2026-11-08T11:00:00.000Z", "3 days", ""},
		{"a version published just after the cutoff and its clock skew is too young to be the newest", ageHead("4.6.7"), "2026-11-09T12:05:01.000Z", "3 days", ""},
		{"a version published just before the cutoff and its clock skew is the newest, so the pick is refused", ageHead("4.6.7"), "2026-11-09T12:04:59.000Z", "3 days", refused},
		{"a version published on the cutoff and its clock skew is the newest, so the pick is refused", ageHead("4.6.7"), "2026-11-09T12:05:00.000Z", "3 days", refused},
		{"an age that is not a duration is a refusal", ageHead("4.6.7"), dayOld, "soon", "the minimum release age: soon is not a duration"},
		{"an age longer than the limit is a refusal", ageHead("4.6.7"), dayOld, "3651 days", "the minimum release age: 3651 days is longer than 3650 days"},
	}
	for _, c := range cases {
		code, line := runAge(t, "pnpm-lock.yaml", base, c.head, agePNPMRegistry(c.stamp), c.age)
		switch {
		case c.want == "" && (code != 0 || line != "PASS"):
			t.Errorf("pnpm age: %s: %d %q, want PASS", c.label, code, line)
		case c.want != "" && (code != 1 || !strings.HasPrefix(line, "FAIL: ") || !strings.Contains(line, c.want)):
			t.Errorf("pnpm age: %s: %d %q, want a refusal with %q", c.label, code, line, c.want)
		}
	}
}

func TestMinReleaseAgeRefusalIsWordedAsWithoutIt(t *testing.T) {
	base := agePNPMBase()
	_, plain := runAge(t, "pnpm-lock.yaml", base, ageHead("4.6.7"), agePNPMRegistry(dayOld), "")
	_, aged := runAge(t, "pnpm-lock.yaml", base, ageHead("4.6.6"), agePNPMRegistry(dayOld), "3 days")
	want := `FAIL: zod@4.6.7: 4.6.7 is neither the newest zod inside its range published by the time of the decision nor the newest the base already holds`
	if plain != want {
		t.Errorf("the refusal without an age: %q, want %q", plain, want)
	}
	if aged != strings.ReplaceAll(want, "4.6.7", "4.6.6") {
		t.Errorf("the refusal with an age has the same shape: %q", aged)
	}
}

func ageNPMLock(d, nested, x object) string {
	packages := object{
		"":               object{"name": "demo", "version": "1.0.0", "dependencies": object{"d": "^1.0.0"}},
		"node_modules/d": d,
		"node_modules/x": x,
	}
	if nested != nil {
		packages["node_modules/d/node_modules/x"] = nested
	}
	data, _ := json.MarshalIndent(object{"name": "demo", "version": "1.0.0", "lockfileVersion": 3, "requires": true, "packages": packages}, "", "  ")
	return string(data)
}

func TestMinReleaseAgeNPM(t *testing.T) {
	base := ageNPMLock(lockEntry("d", "1.0.0", intA, object{"dependencies": object{"x": "^1.0.0"}}), nil, lockEntry("x", "1.9.0", intB, nil))
	reg := func(picked string) registry {
		return registry{
			manifests: map[string]object{
				"d@1.0.1":     manifestOf("d", "1.0.1", intD, object{"dependencies": object{"x": "~1.0.0"}}),
				"x@" + picked: manifestOf("x", picked, intC, nil),
			},
			packuments: map[string]object{
				"d": packumentOf("d", "1.0.0", "1.0.1"),
				"x": {"name": "x", "time": object{"1.0.0": longAgo, "1.0.5": longAgo, "1.0.9": dayOld, "1.9.0": longAgo},
					"versions": object{"1.0.0": object{}, "1.0.5": object{}, "1.0.9": object{}, "1.9.0": object{}}},
			},
		}
	}
	head := func(picked string) string {
		return ageNPMLock(lockEntry("d", "1.0.1", intD, object{"dependencies": object{"x": "~1.0.0"}}),
			lockEntry("x", picked, intC, nil), lockEntry("x", "1.9.0", intB, nil))
	}
	cases := []struct {
		label  string
		picked string
		age    string
		want   string
	}{
		{"a new nested copy one patch behind a day-old version, with an age of 3 days", "1.0.5", "3 days", ""},
		{"the same without an age", "1.0.5", "", refused},
		{"the day-old version itself, with an age of 3 days", "1.0.9", "3 days", refused},
		{"an older copy than the newest old enough, with an age of 3 days", "1.0.0", "3 days", refused},
		{"the day-old version, with an age of 1 hour", "1.0.9", "1 hour", ""},
	}
	for _, c := range cases {
		code, line := runAge(t, "package-lock.json", base, head(c.picked), reg(c.picked), c.age)
		switch {
		case c.want == "" && (code != 0 || line != "PASS"):
			t.Errorf("npm age: %s: %d %q, want PASS", c.label, code, line)
		case c.want != "" && (code != 1 || !strings.HasPrefix(line, "FAIL: ") || !strings.Contains(line, c.want)):
			t.Errorf("npm age: %s: %d %q, want a refusal with %q", c.label, code, line, c.want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	accepted := []struct {
		text    string
		minutes int64
	}{
		{"4320", 4320}, {"0", 0}, {"3 days", 4320}, {"3days", 4320}, {"3 Days", 4320}, {"3 DAYS", 4320}, {"1 day", 1440},
		{"3d", 4320}, {"72 hours", 4320}, {"72h", 4320}, {"72 hrs", 4320}, {"1 hr", 60}, {"90 minutes", 90}, {"90 mins", 90},
		{"1 min", 1}, {"90m", 90}, {"1 week", 10080}, {"2 weeks", 20160}, {"2w", 20160}, {"1 hour", 60},
		{"0007", 7}, {"5256000", 5256000}, {"3650 days", 3650 * 1440}, {"521 weeks", 521 * 10080}, {"521w", 521 * 10080},
	}
	for _, c := range accepted {
		var got time.Duration
		if line := core.Guard(func() { got = parseDuration(c.text, "age") }); line != "" || got != time.Duration(c.minutes)*time.Minute {
			t.Errorf("age %q: %v %q, want %d minutes", c.text, got, line, c.minutes)
		}
	}
	refusedTexts := []string{"", " ", "-1", "+1", "3 months", "3 month", "3mo", "1 year", "1y", "soon", "3.5 days", "1e3", "3  days", " 3 days",
		"3 days ", "3\tdays", "3 days\n", "3\n", "3 day s", "٣ days", "3 дays", "3 dayſ", "3 hourſ", "3 Khours",
		"12345678", "5256001", "3651 days", "522 weeks", "5200000 weeks", "9999999 days", "days", "d", "0x10", "3_0", "1 .5 days"}
	for _, text := range refusedTexts {
		line := core.Guard(func() { parseDuration(text, "age") })
		if !strings.HasPrefix(line, "FAIL: ") || (!strings.Contains(line, "is not a duration") && !strings.Contains(line, "is longer than 3650 days")) {
			t.Errorf("age %q is refused as a duration: %q", text, line)
		}
	}
	if line := core.Guard(func() { parseDuration("3651 days", "age") }); line != "FAIL: age: 3651 days is longer than 3650 days" {
		t.Errorf("too long: %q", line)
	}
	if line := core.Guard(func() { parseDuration("soon", "age") }); line != "FAIL: age: soon is not a duration" {
		t.Errorf("not a duration: %q", line)
	}
}

func TestMinReleaseAgeOption(t *testing.T) {
	base, head := string(lockText("1.0.0", intA)), string(lockText("1.0.1", intB))
	reg := registry{
		manifests:  map[string]object{"foo@1.0.1": manifestOf("foo", "1.0.1", intB, nil)},
		packuments: map[string]object{"foo": packumentOf("foo", "1.0.0", "1.0.1")},
	}
	run := func(options ...string) (int, string) {
		var out bytes.Buffer
		code := runMainWith(t, &out, "package-lock.json", base, head, reg, nil, options...)
		return code, strings.TrimSpace(out.String())
	}
	if code, out := run("--before", ageNow, "--min-release-age", "3 days"); code != 0 || out != "PASS" {
		t.Errorf("--min-release-age is read and the pair still passes: %d %q", code, out)
	}
	if code, out := run("--before", ageNow, "--min-release-age", "soon"); code != 1 || out != "FAIL: the minimum release age: soon is not a duration" {
		t.Errorf("--min-release-age that is not a duration is a refusal: %d %q", code, out)
	}
	if code, out := run("--min-release-age", "soon"); code != 1 || out != "FAIL: the minimum release age: soon is not a duration" {
		t.Errorf("an age that is not a duration is refused even with nothing new to hold: %d %q", code, out)
	}
	if code, out := run("--min-release-age", "1", "--min-release-age", "1"); code != 1 || !strings.HasPrefix(out, "FAIL: usage: ") {
		t.Errorf("--min-release-age given twice is a usage refusal: %d %q", code, out)
	}
	if code, out := run("--min-release-age"); code != 1 || !strings.HasPrefix(out, "FAIL: usage: ") {
		t.Errorf("--min-release-age with nothing after it is a usage refusal: %d %q", code, out)
	}
}
