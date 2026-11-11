package core

import (
	"errors"
	"io"
)

// What published_before reads of a packument, and nothing else: the multi-megabyte manifests of
// every version are validated as they stream by and dropped, so a packument costs the size of its
// `time` map and its version names, not of its text.

type VersionEntry struct {
	Name       string
	IsObject   bool
	Deprecated bool
}

type Packument struct {
	Name     *Value // nil when absent
	Time     *Value // nil when absent
	Versions []VersionEntry
	HasVers  bool // `versions` is a mapping
}

// maxMemberChars caps one member of a packument: a top-level member, or one version under
// `versions`. It is judged in characters of the raw text, from the member's first character.
var maxMemberChars int64 = 8 * 1024 * 1024

// key reads the key of the object member at the read position, which holds a string; the key counts
// against the member cap like a value does.
func (j *jsonReader) key() string {
	j.skipSpace()
	if b, ok := j.peek(); !ok || b != '"' {
		j.failNow()
	}
	var key string
	j.member(func() {
		j.next()
		key = j.readString(true)
	})
	j.skipSpace()
	if j.next() != ':' {
		j.fail()
	}
	return key
}

// packument reads one packument. Its top-level object and its `versions` object are walked member
// by member, each member value read alone, so the text read stays about one member.
func (j *jsonReader) packument() *Packument {
	p := &Packument{}
	j.skipSpace()
	if b, ok := j.peek(); !ok || b != '{' {
		j.member(j.skip)
		j.end()
		return p
	}
	j.next()
	var viol string
	seen := map[string]struct{}{}
	j.skipSpace()
	if c, _ := j.peek(); c == '}' {
		j.next()
	} else {
		for {
			key := j.key()
			j.hook = true
			switch key {
			case "name":
				j.member(func() { p.Name = j.value() })
			case "time":
				j.member(func() { p.Time = j.value() })
			case "versions":
				j.skipSpace()
				if b, _ := j.peek(); b == '{' {
					p.Versions, p.HasVers = j.versions(), true
				} else {
					j.member(j.skip)
				}
			default:
				j.member(j.skip)
			}
			j.hook = false
			violation(seen, key, &viol)
			seen[key] = struct{}{}
			j.skipSpace()
			c := j.next()
			if c == '}' {
				break
			}
			if c != ',' {
				j.fail()
			}
		}
	}
	if viol != "" {
		Reject(viol)
	}
	j.end()
	if _, d := seen["dist"]; d {
		if _, v := seen["version"]; v {
			// The object hook reduces the document to its `deprecated` field.
			return &Packument{}
		}
	}
	return p
}

// versions reads the `versions` mapping: each manifest down to whether it is an object and whether
// its `deprecated` field is truthy.
func (j *jsonReader) versions() []VersionEntry {
	j.next() // {
	var out []VersionEntry
	var viol string
	seen := map[string]struct{}{}
	j.skipSpace()
	if c, _ := j.peek(); c == '}' {
		j.next()
		return out
	}
	for {
		entry := VersionEntry{Name: j.key()}
		j.member(func() {
			if b, _ := j.peek(); b == '{' {
				entry.IsObject = true
				entry.Deprecated = j.manifestDeprecated()
			} else {
				j.skip()
			}
		})
		violation(seen, entry.Name, &viol)
		seen[entry.Name] = struct{}{}
		out = append(out, entry)
		j.skipSpace()
		c := j.next()
		if c == '}' {
			break
		}
		if c != ',' {
			j.fail()
		}
	}
	if viol != "" {
		Reject(viol)
	}
	_, hasDist := seen["dist"]
	_, hasVersion := seen["version"]
	if hasDist && hasVersion {
		// The hook turns the mapping into {'deprecated': <its own 'deprecated' entry>}.
		reduced := VersionEntry{Name: "deprecated"}
		for _, e := range out {
			if e.Name == "deprecated" {
				reduced.IsObject, reduced.Deprecated = e.IsObject, e.Deprecated
			}
		}
		return []VersionEntry{reduced}
	}
	return out
}

func (j *jsonReader) manifestDeprecated() bool {
	j.next() // {
	j.enter()
	defer j.leave()
	dep := false
	var viol string
	seen := map[string]struct{}{}
	j.skipSpace()
	if c, _ := j.peek(); c == '}' {
		j.next()
		return false
	}
	for {
		j.skipSpace()
		if j.next() != '"' {
			j.fail()
		}
		key := j.readString(true)
		j.skipSpace()
		if j.next() != ':' {
			j.fail()
		}
		if key == "deprecated" {
			dep = Truthy(j.value())
		} else {
			j.skip()
		}
		violation(seen, key, &viol)
		seen[key] = struct{}{}
		j.skipSpace()
		c := j.next()
		if c == '}' {
			break
		}
		if c != ',' {
			j.fail()
		}
	}
	if viol != "" {
		Reject(viol)
	}
	return dep
}

// ParsePackument reads a packument from a stream while it arrives, as far as the stream goes: more
// than limit bytes, or a member over the member cap, is a refusal at the point it is found. A read
// error other than the end of the stream is returned to the caller untouched.
func ParsePackument(r io.Reader, name string, limit int64) (p *Packument, readErr error) {
	cr := &countingReader{r: r, limit: limit, msg: "the registry packument of " + Q(name) + " is too large"}
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		if cr.err != nil {
			p, readErr = nil, cr.err
			return
		}
		if _, syntax := rec.(jsonSyntax); syntax {
			Reject("the registry packument of " + Q(name) + " is not JSON")
		}
		panic(rec)
	}()
	j := newJSONReader(cr, true)
	j.memberCap = maxMemberChars
	j.memberMsg = "a member of the registry packument of " + Q(name) + " is too large"
	p = j.packument()
	if cr.err != nil {
		return nil, cr.err
	}
	return p, nil
}

type countingReader struct {
	r     io.Reader
	n     int64
	limit int64
	msg   string
	err   error
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		c.err = err
	}
	if c.n > c.limit {
		Reject(c.msg)
	}
	return n, err
}
