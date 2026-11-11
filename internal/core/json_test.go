package core

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func refusal(t *testing.T, fn func()) string {
	t.Helper()
	line := Guard(fn)
	if line == "" {
		t.Fatalf("expected a refusal")
	}
	return line
}

func TestParseJSONHook(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"a": 1, "a": 2}`, "FAIL: duplicate key a"},
		{`{"__proto__": 1}`, "FAIL: the key __proto__"},
		{`{"x": {"constructor": 1}}`, "FAIL: the key constructor"},
		{`{"a": NaN}`, "FAIL: a non-finite number"},
		{`[Infinity]`, "FAIL: a non-finite number"},
		{`[-Infinity]`, "FAIL: a non-finite number"},
	}
	for _, c := range cases {
		got := refusal(t, func() { ParseJSON([]byte(c.in), false) })
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.in, got, c.want)
		}
	}
	// A syntax error later in the document wins over an object that was not finished.
	if _, ok := ParseJSON([]byte(`{"a": 1, "a": 2, bad`), false); ok {
		t.Errorf("not JSON must not parse")
	}
}

func TestParseJSONSyntax(t *testing.T) {
	for _, in := range []string{``, `{`, `[1,]`, `{"a": 01}`, `1.`, `"\x"`, "\"a\x01\"", `{"a" 1}`, `[] []`, "\xef\xbb\xbf{}", `{'a': 1}`, "\"\xff\""} {
		if _, ok := ParseJSON([]byte(in), false); ok {
			t.Errorf("%q must not parse", in)
		}
	}
	for _, in := range []string{` {} `, `[1, 2.5e-3, -0, true, null, "é😀"]`, `{"a": {"b": []}}`} {
		if _, ok := ParseJSON([]byte(in), false); !ok {
			t.Errorf("%q must parse", in)
		}
	}
	deep := strings.Repeat("[", maxJSONDepth+1) + strings.Repeat("]", maxJSONDepth+1)
	if _, ok := ParseJSON([]byte(deep), false); ok {
		t.Errorf("a document nested past the limit must not parse")
	}
}

func TestNumbersCompareByKind(t *testing.T) {
	v, _ := ParseJSON([]byte(`[1, 1.0, true, -0, 0, 1e0]`), false)
	l := v.L
	if CanonEq(l[0], l[1]) || CanonEq(l[0], l[2]) || !CanonEq(l[3], l[4]) || !CanonEq(l[1], l[5]) {
		t.Errorf("canonical equality must tell 1, 1.0 and true apart")
	}
	if !PyEq(l[0], l[1]) || !PyEq(l[0], l[2]) {
		t.Errorf("PyEq equates 1, 1.0 and true")
	}
}

func packumentText(versions int) string {
	var b strings.Builder
	b.WriteString(`{"name":"big","versions":{`)
	for i := 0; i < versions; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"1.0.%d":{"name":"big","version":"1.0.%d","dist":{"integrity":"x","tarball":"y"},"readme":"%s","dependencies":{"a":"^1"}}`,
			i, i, strings.Repeat("r", 4000))
	}
	b.WriteString(`},"time":{`)
	for i := 0; i < versions; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"1.0.%d":"2020-01-01T00:00:00.000Z"`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

func TestPackumentIsStreamed(t *testing.T) {
	text := packumentText(5000) // about 20 MB of text
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var peak uint64
	p, err := ParsePackument(&sampling{r: strings.NewReader(text), peak: &peak}, "big", 64<<20)
	runtime.ReadMemStats(&after)
	if err != nil || p.Name == nil || len(p.Versions) != 5000 || p.Time.M.Len() != 5000 {
		t.Fatalf("packument not read: %v", err)
	}
	if peak > before.HeapAlloc && peak-before.HeapAlloc > 8<<20 {
		t.Errorf("a %d MB packument grew the heap by %d MB while it was read; it must be streamed", len(text)>>20, (peak-before.HeapAlloc)>>20)
	}
}

// sampling records the highest heap seen while the reader is being drained.
type sampling struct {
	r    *strings.Reader
	peak *uint64
	n    int
}

func (s *sampling) Read(b []byte) (int, error) {
	s.n++
	if s.n%64 == 0 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		if m.HeapAlloc > *s.peak {
			*s.peak = m.HeapAlloc
		}
	}
	return s.r.Read(b)
}

func TestPackumentRules(t *testing.T) {
	read := func(s string, limit int64) (*Packument, string) {
		var p *Packument
		line := Guard(func() { p, _ = ParsePackument(strings.NewReader(s), "p", limit) })
		return p, line
	}
	if _, line := read(`{"name":"p","versions":{"1.0.0":{"dist":{},"version":"1.0.0","deprecated":"gone"}},"time":{"1.0.0":"x"}}`, 1<<20); line != "" {
		t.Errorf("a packument must read: %s", line)
	}
	p, _ := read(`{"name":"p","versions":{"1.0.0":{"dist":{},"version":"1.0.0","deprecated":"gone"},"2.0.0":{},"3.0.0":3},"time":{}}`, 1<<20)
	if !p.Versions[0].Deprecated || p.Versions[1].Deprecated || !p.Versions[1].IsObject || p.Versions[2].IsObject {
		t.Errorf("deprecated and object flags: %+v", p.Versions)
	}
	if _, line := read(`{"name":"p","versions":{"1.0.0":{"a":1,"a":2}}}`, 1<<20); line != "FAIL: duplicate key a" {
		t.Errorf("a duplicate key in a manifest: %q", line)
	}
	if _, line := read(`{"name":"p"`+strings.Repeat(" ", 100)+`,bad`, 50); line != "FAIL: the registry packument of p is too large" {
		t.Errorf("the size is judged before the content: %q", line)
	}
	if _, line := read(`{"name":"p"`, 1<<20); line != "FAIL: the registry packument of p is not JSON" {
		t.Errorf("not JSON: %q", line)
	}
	if p, _ := read(`{"name":"p","dist":1,"version":2}`, 1<<20); p.Name != nil {
		t.Errorf("an object with dist and version is reduced to its deprecated field")
	}
}
