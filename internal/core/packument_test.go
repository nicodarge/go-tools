package core

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// bs starts a JSON escape; written apart so the escapes in these fixtures stay escapes.
const bs = "\\"

const longAgo = "2020-01-01T00:00:00.000Z"

// pieces serves its text size bytes at a time, as a network answer arrives.
type pieces struct {
	text []byte
	size int
	// served counts the bytes handed out.
	served int
	// err is returned once the text is served, in place of the end of the stream.
	err error
}

func (p *pieces) Read(b []byte) (int, error) {
	if len(p.text) == 0 {
		if p.err != nil {
			return 0, p.err
		}
		return 0, io.EOF
	}
	n := min(p.size, len(b), len(p.text))
	copy(b, p.text[:n])
	p.text = p.text[n:]
	p.served += n
	return n, nil
}

func inPieces(text string, size int) *pieces { return &pieces{text: []byte(text), size: size} }

// readPackument is what ParsePackument answers: the packument, or the refusal line.
func readPackument(r io.Reader) (*Packument, string) {
	var p *Packument
	line := Guard(func() {
		var err error
		if p, err = ParsePackument(r, "x", 64<<20); err != nil {
			Reject("read error: " + err.Error())
		}
	})
	return p, line
}

func TestPackumentReadsTheSameAtEveryPieceSize(t *testing.T) {
	shapes := map[string]string{
		"plain": `{"name": "x", "time": {"created": "` + longAgo + `", "1.0.0": "` + longAgo + `"},
			"versions": {"1.0.0": {"dist": {"integrity": "sha512-x"}, "version": "1.0.0"},
			"1.1.0": {"name": "x", "deprecated": "café 😀", "dist": {}, "version": "1.1.0"}}}`,
		"other members dropped": `{"readme": "` + strings.Repeat("text ", 500) + `", "count": 12345, "ratio": -1.5e3,
			"flag": true, "none": null, "users": {"a": true, "b": [1, 2, {"c": "d"}]}, "name": "x",
			"time": {"1.0.0": "` + longAgo + `"}, "versions": {"1.0.0": {}}}`,
		"numbers":                `{"name":"x","a":1,"b":22,"c":333,"d":-4.5,"e":1.5e-7,"f":2E+10,"time":{},"versions":{}}`,
		"versions last":          `{"time":{"1.0.0":"` + longAgo + `"},"name":"x","versions":{"1.0.0":{"deprecated":"old"}}}`,
		"versions not an object": `{"name":"x","time":{},"versions":[]}`,
		"empty members":          `{"name":"x","time":{},"versions":{}}`,
		"escapes": `{"name":"x","time":{},"versions":{"1.0.0":{"deprecated":"caf` + bs + `u00e9 ` + bs + `ud83d` + bs + `ude00 ` +
			bs + `ud83d lone ` + bs + `n"}}}`,
		"non-ASCII text, compact": "{\"name\":\"x\",\"time\":{},\"versions\":{\"1.0.0\":{\"deprecated\":\"café \U0001f600\"}}}",
	}
	for label, text := range shapes {
		want, line := readPackument(strings.NewReader(text))
		if line != "" || want.Name == nil || want.Name.S != "x" {
			t.Fatalf("%s: %q %+v", label, line, want)
		}
		for _, size := range []int{1, 2, 3, 5, 64, len(text)} {
			got, line := readPackument(inPieces(text, size))
			if line != "" || !reflect.DeepEqual(got, want) {
				t.Errorf("%s in pieces of %d bytes: %q %+v, want %+v", label, size, line, got, want)
			}
		}
	}
	trimmed, _ := readPackument(inPieces(`{"name":"x","readme":"r","users":{},"time":{},"versions":{}}`, 4))
	if trimmed.Time == nil || !trimmed.HasVers {
		t.Errorf("the members that are read are kept: %+v", trimmed)
	}
}

func TestPackumentRefusals(t *testing.T) {
	cases := []struct{ label, text, want string }{
		{"a trailing comma", `{"name": "x",}`, "is not JSON"},
		{"a missing comma", `{"name": "x" "time": {}}`, "is not JSON"},
		{"a missing colon", `{"name" "x"}`, "is not JSON"},
		{"a key that is not a string", `{1: "x"}`, "is not JSON"},
		{"a missing value", `{"name": }`, "is not JSON"},
		{"data after the object", `{"name": "x"} []`, "is not JSON"},
		{"a truncated object", `{"name": "x", "time": {`, "is not JSON"},
		{"a truncated number", `{"name": "x", "n": 1`, "is not JSON"},
		{"a truncated multi-byte character", "{\"name\": \"x\", \"r\": \"é"[:len("{\"name\": \"x\", \"r\": \"")+1], "is not JSON"},
		{"bytes that are not UTF-8", "{\"name\": \"x\", \"r\": \"\xff\"}", "is not JSON"},
		{"a byte order mark", "\xef\xbb\xbf{\"name\": \"x\"}", "is not JSON"},
		{"a non-finite number", `{"name": "x", "n": NaN}`, "a non-finite number"},
		{"an empty answer", ``, "is not JSON"},
		{"a duplicate top-level key", `{"name": "x", "name": "x"}`, "duplicate key"},
		{"a duplicate key in a dropped member", `{"name": "x", "users": {"a": 1, "a": 2}}`, "duplicate key"},
		{"a duplicate version", `{"name": "x", "versions": {"1.0.0": {}, "1.0.0": {}}}`, "duplicate key"},
		{"a forbidden key", `{"name": "x", "versions": {"__proto__": {}}}`, "the key"},
		{"a version that repeats a key", `{"name": "x", "versions": {"1.0.0": {"a": 1, "a": 2}}}`, "duplicate key"},
	}
	for _, c := range cases {
		for _, size := range []int{1, 7, max(len(c.text), 1)} {
			_, line := readPackument(inPieces(c.text, size))
			if !strings.HasPrefix(line, "FAIL: ") || !strings.Contains(line, c.want) {
				t.Errorf("%s, in pieces of %d bytes: %q, want a refusal with %q", c.label, size, line, c.want)
			}
		}
	}
	for _, text := range []string{`[]`, `{}`, `{"name": "y"}`} {
		if p, line := readPackument(strings.NewReader(text)); line != "" || (p.Name != nil && p.Name.S == "x") {
			t.Errorf("%s reads, and is judged by its caller: %q %+v", text, line, p)
		}
	}
}

func TestPackumentSizeLimit(t *testing.T) {
	read := func(text string, limit int64, size int) string {
		_, line := func() (p *Packument, line string) {
			line = Guard(func() { p, _ = ParsePackument(inPieces(text, size), "x", limit) })
			return
		}()
		return line
	}
	if got := read(`{"name":"x"`+strings.Repeat(" ", 100)+`,bad`, 50, 16); got != "FAIL: the registry packument of x is too large" {
		t.Errorf("a stream over the limit refuses where the limit is passed: %q", got)
	}
	if got := read(`{"name":"x"}`+strings.Repeat(" ", 100), 50, 16); got != "FAIL: the registry packument of x is too large" {
		t.Errorf("trailing space is read to the end, and counts: %q", got)
	}
	if got := read(`{"name":"x"}`, 12, 5); got != "" {
		t.Errorf("a stream of exactly the limit reads: %q", got)
	}
	if got := read(`{"name":"x"`, 5000, 5); got != "FAIL: the registry packument of x is not JSON" {
		t.Errorf("not JSON: %q", got)
	}
	// The first fault found is the refusal: an early syntax error is not hidden by what follows it.
	if got := read(`{"name" "x"}`+strings.Repeat(" ", 100), 50, 5); got != "FAIL: the registry packument of x is not JSON" {
		t.Errorf("a structural error found before the limit is passed: %q", got)
	}
}

func TestPackumentMemberCap(t *testing.T) {
	defer func(old int64) { maxMemberChars = old }(maxMemberChars)
	maxMemberChars = 1500
	const tooLarge = "FAIL: a member of the registry packument of x is too large"
	doc := func(member string, filler int) string {
		return fmt.Sprintf(`{"name": "x", %q: %q}`, member, strings.Repeat("r", filler))
	}
	many := `{"name": "x", "time": {}, "versions": {`
	for n := range 400 {
		if n > 0 {
			many += ","
		}
		many += fmt.Sprintf(`"1.0.%d": {"deprecated": "%s"}`, n, strings.Repeat("d", 50))
	}
	many += `}}`
	cases := []struct{ label, text, want string }{
		{"a member larger than the limit", doc("readme", 5000), tooLarge},
		{"a member between the limit and twice the limit", doc("readme", 1800), tooLarge},
		{"a member a little over the limit, whole", doc("readme", 1499), tooLarge},
		{"a member of the size of the limit", doc("readme", 1498), ""},
		{"a key larger than the limit", fmt.Sprintf(`{"name": "x", %q: 1}`, strings.Repeat("k", 2000)), tooLarge},
		{"a version larger than the limit", `{"name": "x", "versions": {"1.0.0": {"readme": "` + strings.Repeat("r", 2000) + `"}}}`, tooLarge},
		{"a versions member that is not an object", `{"name": "x", "versions": "` + strings.Repeat("v", 2000) + `"}`, tooLarge},
		{"a top-level value that is not an object", `"` + strings.Repeat("v", 2000) + `"`, tooLarge},
		{"a member too large and not JSON", `{"name": "x", "readme": "` + strings.Repeat("r", 2000) + "\x01\"}", tooLarge},
		{"a member not JSON that ends before the limit", `{"name": "x", "readme": "` + strings.Repeat("r", 100) + "\x01\"}", "is not JSON"},
		{"a member too large and cut off", `{"name": "x", "readme": "` + strings.Repeat("r", 2000), tooLarge},
		{"many members each under the limit", many, ""},
	}
	for _, c := range cases {
		for _, size := range []int{64, 10000} {
			p, line := readPackument(inPieces(c.text, size))
			switch {
			case c.want == "" && line != "":
				t.Errorf("%s, in pieces of %d bytes: %q", c.label, size, line)
			case c.want == "" && c.label == "many members each under the limit" && len(p.Versions) != 400:
				t.Errorf("%s: %d versions", c.label, len(p.Versions))
			case c.want != "" && !strings.Contains(line, strings.TrimPrefix(c.want, "FAIL: ")):
				t.Errorf("%s, in pieces of %d bytes: %q, want %q", c.label, size, line, c.want)
			}
		}
	}

	// A member over the cap is refused as soon as it passes it, not read on to the end of the answer.
	huge := inPieces(`{"name": "x", "readme": "`+strings.Repeat("r", 100000)+`"}`, 64)
	if _, line := readPackument(huge); line != tooLarge {
		t.Fatalf("a member far over the cap: %q", line)
	}
	if huge.served > 1500+200 {
		t.Errorf("a member over the cap was read on to %d bytes", huge.served)
	}
}

func TestPackumentMemberCharactersAreNotBytes(t *testing.T) {
	defer func(old int64) { maxMemberChars = old }(maxMemberChars)
	maxMemberChars = 100
	// 40 characters of 3 bytes each, escaped ones counting as the 6 characters they are written with.
	member := strings.Repeat("€", 40)
	if _, line := readPackument(strings.NewReader(`{"name": "x", "readme": "` + member + `"}`)); line != "" {
		t.Errorf("a member is judged in characters: %q", line)
	}
	if _, line := readPackument(strings.NewReader(`{"name": "x", "readme": "` + strings.Repeat(`\u20ac`, 20) + `"}`)); line != "FAIL: a member of the registry packument of x is too large" {
		t.Errorf("an escape counts as the characters it is written with: %q", line)
	}
}

func TestPackumentReadErrors(t *testing.T) {
	reset := errors.New("connection reset")
	for _, text := range []string{`{"name": "x", "time": `, `{"name": "x", "time": {"1.0.0": "`, `{"name": "x", "versions": {"1.0.0": {`} {
		p := inPieces(text, 5)
		p.err = reset
		got, err := ParsePackument(p, "x", 1<<20)
		if got != nil || !errors.Is(err, reset) {
			t.Errorf("%q: an answer that breaks off is the caller's read error: %+v %v", text, got, err)
		}
	}
}

func TestPackumentDamagedDocumentsAreRefusedAlike(t *testing.T) {
	seeds := []string{
		`{"name": "x", "time": {"1.0.0": "` + longAgo + `"}, "versions": {"1.0.0": {"dist": {}, "version": "1.0.0", "deprecated": "d"}}}`,
		`{"readme": "text", "count": 12345, "ratio": -1.5e3, "users": {"a": true, "b": [1, 2, {"c": "d"}]}, "name": "x", "time": {}, "versions": {"1.0.0": {}}}`,
		`{"name":"x","a":1,"b":22,"d":-4.5,"e":1.5e-7,"f":2E+10,"time":{},"versions":{}}`,
	}
	damage := []string{"{", "}", "[", "]", ",", ":", `"`, `\`, " ", "-", ".", "\xc3", `"versions"`}
	// Every document with one byte inserted, deleted or replaced, read whole and in pieces.
	same := func(raw string) {
		want, wantLine := readPackument(strings.NewReader(raw))
		for _, size := range []int{1, 64} {
			got, gotLine := readPackument(inPieces(raw, size))
			if gotLine != wantLine || !reflect.DeepEqual(got, want) {
				t.Fatalf("%q read in pieces of %d bytes: %q %+v, whole: %q %+v", raw, size, gotLine, got, wantLine, want)
			}
		}
	}
	for _, seed := range seeds {
		for at := 0; at <= len(seed); at++ {
			for _, d := range damage {
				same(seed[:at] + d + seed[at:])
				if at < len(seed) {
					same(seed[:at] + d + seed[at+1:])
				}
			}
			if at < len(seed) {
				same(seed[:at] + seed[at+1:])
			}
		}
	}
}
