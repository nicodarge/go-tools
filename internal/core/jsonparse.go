package core

import (
	"bufio"
	"bytes"
	"io"
	"unicode/utf8"
)

// A strict JSON reader with the checker's object hook (no duplicate key, no __proto__ or
// constructor) and no NaN or Infinity. It works on a stream, so a document is never needed in memory as bytes, and it can parse a part
// of a document into a Value while only validating the rest.

var forbiddenKeys = []string{"__proto__", "constructor"}

const maxJSONDepth = 990

type jsonSyntax struct{}

type jsonReader struct {
	r     *bufio.Reader
	depth int
	// hook applies the packument object hook: an object with both `dist` and `version` is reduced
	// to its `deprecated` field.
	hook bool

	// chars counts the characters read so far. While capped is set, a member of a packument that
	// runs past memberCap characters from memberStart is refused as soon as it does.
	chars       int64
	capped      bool
	memberStart int64
	memberCap   int64
	memberMsg   string
}

func newJSONReader(r io.Reader, hook bool) *jsonReader {
	return &jsonReader{r: bufio.NewReaderSize(r, 64*1024), hook: hook}
}

// failNow refuses the document as not JSON at once.
func (j *jsonReader) failNow() { panic(jsonSyntax{}) }

// fail refuses the document as not JSON. Inside a capped member it first reads on to see whether
// the member is over the cap: a member too large to judge is refused as too large, whatever is
// wrong in it.
func (j *jsonReader) fail() {
	for j.capped && j.chars-j.memberStart <= j.memberCap {
		b, err := j.r.ReadByte()
		if err != nil {
			break
		}
		if b&0xC0 != 0x80 {
			j.chars++
		}
	}
	j.checkMember()
	j.failNow()
}

func (j *jsonReader) checkMember() {
	if j.capped && j.chars-j.memberStart > j.memberCap {
		Reject(j.memberMsg)
	}
}

// member reads one member of a packument with the cap applied, from its first character that is
// not whitespace.
func (j *jsonReader) member(read func()) {
	j.skipSpace()
	j.memberStart, j.capped = j.chars, true
	read()
	j.checkMember()
	j.capped = false
}

func (j *jsonReader) peek() (byte, bool) {
	b, err := j.r.Peek(1)
	if err != nil {
		return 0, false
	}
	return b[0], true
}

func (j *jsonReader) next() byte {
	b, err := j.r.ReadByte()
	if err != nil {
		j.fail()
	}
	if b&0xC0 != 0x80 {
		j.chars++
		j.checkMember()
	}
	return b
}

func (j *jsonReader) skipSpace() {
	for {
		b, ok := j.peek()
		if !ok || (b != ' ' && b != '\t' && b != '\n' && b != '\r') {
			return
		}
		_, _ = j.r.ReadByte()
		j.chars++
	}
}

func (j *jsonReader) expect(word string) {
	for i := 0; i < len(word); i++ {
		if j.next() != word[i] {
			j.fail()
		}
	}
}

func (j *jsonReader) hex4() rune {
	var v rune
	for i := 0; i < 4; i++ {
		c := j.next()
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | rune(c-'A'+10)
		default:
			j.fail()
		}
	}
	return v
}

// readString reads a string after its opening quote. With build false nothing is kept.
func (j *jsonReader) readString(build bool) string {
	var buf []byte
	for {
		c := j.next()
		switch {
		case c == '"':
			return string(buf)
		case c < 0x20:
			j.fail()
		case c == '\\':
			e := j.next()
			var r rune
			switch e {
			case '"', '\\', '/':
				r = rune(e)
			case 'b':
				r = '\b'
			case 'f':
				r = '\f'
			case 'n':
				r = '\n'
			case 'r':
				r = '\r'
			case 't':
				r = '\t'
			case 'u':
				r = j.hex4()
				if r >= 0xD800 && r <= 0xDBFF {
					if b, err := j.r.Peek(2); err == nil && b[0] == '\\' && b[1] == 'u' {
						_, _ = j.r.Discard(2)
						j.chars += 2
						low := j.hex4()
						if low >= 0xDC00 && low <= 0xDFFF {
							r = 0x10000 + (r-0xD800)<<10 + (low - 0xDC00)
						} else {
							// A lone surrogate is not ASCII; neither is its replacement U+FFFD.
							if build {
								buf = utf8.AppendRune(buf, utf8.RuneError)
							}
							r = low
						}
					}
				}
				if r >= 0xD800 && r <= 0xDFFF {
					r = utf8.RuneError
				}
			default:
				j.fail()
			}
			if build {
				buf = utf8.AppendRune(buf, r)
			}
		case c < 0x80:
			if build {
				buf = append(buf, c)
			}
		default:
			// A multi-byte sequence must be valid UTF-8.
			n := 0
			switch {
			case c >= 0xC2 && c <= 0xDF:
				n = 1
			case c >= 0xE0 && c <= 0xEF:
				n = 2
			case c >= 0xF0 && c <= 0xF4:
				n = 3
			default:
				j.failNow()
			}
			seq := []byte{c}
			for i := 0; i < n; i++ {
				seq = append(seq, j.next())
			}
			if !utf8.Valid(seq) {
				j.failNow()
			}
			if build {
				buf = append(buf, seq...)
			}
		}
	}
}

func (j *jsonReader) readNumber() string {
	var buf []byte
	take := func() byte {
		b := j.next()
		buf = append(buf, b)
		return b
	}
	digits := func() {
		for {
			b, ok := j.peek()
			if !ok || b < '0' || b > '9' {
				return
			}
			take()
		}
	}
	if b, _ := j.peek(); b == '-' {
		take()
	}
	b, ok := j.peek()
	if !ok {
		j.fail()
	}
	switch {
	case b == '0':
		take()
	case b >= '1' && b <= '9':
		digits()
	default:
		j.fail()
	}
	if p, err := j.r.Peek(2); err == nil && p[0] == '.' && p[1] >= '0' && p[1] <= '9' {
		take()
		digits()
	}
	if p, err := j.r.Peek(3); err == nil || len(p) >= 2 {
		if len(p) >= 2 && (p[0] == 'e' || p[0] == 'E') {
			if p[1] >= '0' && p[1] <= '9' {
				take()
				digits()
			} else if (p[1] == '+' || p[1] == '-') && len(p) == 3 && p[2] >= '0' && p[2] <= '9' {
				take()
				take()
				digits()
			}
		}
	}
	return string(buf)
}

func violation(seen map[string]struct{}, key string, viol *string) {
	if *viol != "" {
		return
	}
	if _, dup := seen[key]; dup {
		*viol = "duplicate key " + Q(key)
		return
	}
	for _, f := range forbiddenKeys {
		if key == f {
			*viol = "the key " + Q(key)
			return
		}
	}
}

func (j *jsonReader) enter() {
	j.depth++
	if j.depth > maxJSONDepth {
		j.failNow()
	}
}

func (j *jsonReader) leave() { j.depth-- }

// value parses any value into a Value.
func (j *jsonReader) value() *Value {
	j.skipSpace()
	b, ok := j.peek()
	if !ok {
		j.fail()
	}
	switch b {
	case '{':
		j.next()
		j.enter()
		defer j.leave()
		m := NewMap()
		var viol string
		seen := map[string]struct{}{}
		j.skipSpace()
		if c, _ := j.peek(); c == '}' {
			j.next()
		} else {
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
				val := j.value()
				violation(seen, key, &viol)
				seen[key] = struct{}{}
				m.Set(key, val)
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
		if j.hook && m.Has("dist") && m.Has("version") {
			reduced := NewMap()
			reduced.Set("deprecated", m.Get("deprecated"))
			m = reduced
		}
		return MapValue(m)
	case '[':
		j.next()
		j.enter()
		defer j.leave()
		v := &Value{K: List}
		j.skipSpace()
		if c, _ := j.peek(); c == ']' {
			j.next()
			return v
		}
		for {
			v.L = append(v.L, j.value())
			j.skipSpace()
			c := j.next()
			if c == ']' {
				return v
			}
			if c != ',' {
				j.fail()
			}
		}
	case '"':
		j.next()
		return StrValue(j.readString(true))
	case 't':
		j.expect("true")
		return &Value{K: Bool, B: true}
	case 'f':
		j.expect("false")
		return &Value{K: Bool}
	case 'n':
		j.expect("null")
		return &Value{K: Null}
	case 'N':
		j.expect("NaN")
		Reject("a non-finite number")
	case 'I':
		j.expect("Infinity")
		Reject("a non-finite number")
	case '-':
		if p, err := j.r.Peek(2); err == nil && p[1] == 'I' {
			j.expect("-Infinity")
			Reject("a non-finite number")
		}
		fallthrough
	default:
		text := j.readNumber()
		canon, isInt := NumCanon(text)
		return &Value{K: Num, S: canon, Int: isInt}
	}
	return nil
}

// skip parses a value only to validate it.
func (j *jsonReader) skip() {
	j.skipSpace()
	b, ok := j.peek()
	if !ok {
		j.fail()
	}
	switch b {
	case '{':
		j.next()
		j.enter()
		defer j.leave()
		var viol string
		seen := map[string]struct{}{}
		j.skipSpace()
		if c, _ := j.peek(); c == '}' {
			j.next()
			return
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
			j.skip()
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
	case '[':
		j.next()
		j.enter()
		defer j.leave()
		j.skipSpace()
		if c, _ := j.peek(); c == ']' {
			j.next()
			return
		}
		for {
			j.skip()
			j.skipSpace()
			c := j.next()
			if c == ']' {
				return
			}
			if c != ',' {
				j.fail()
			}
		}
	case '"':
		j.next()
		j.readString(false)
	default:
		j.value()
	}
}

// end checks that nothing but whitespace follows the document.
func (j *jsonReader) end() {
	j.skipSpace()
	if _, ok := j.peek(); ok {
		j.fail()
	}
}

// ParseJSON reads a whole document into a Value; ok is false when it is not JSON. A Reject from
// the object hook or a non-finite number propagates.
func ParseJSON(data []byte, hook bool) (v *Value, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, is := r.(jsonSyntax); is {
				v, ok = nil, false
				return
			}
			panic(r)
		}
	}()
	j := newJSONReader(bytes.NewReader(data), hook)
	v = j.value()
	j.end()
	return v, true
}

// ASCIIOnly refuses any text or key that is not ASCII, except a `deprecated` message.
func ASCIIOnly(v *Value, key string, hasKey bool, depth int) {
	if depth > 100 {
		Reject("JSON nested too deeply")
	}
	switch KindOf(v) {
	case Str:
		if !IsASCII(v.S) && (!hasKey || key != "deprecated") {
			Reject("non-ASCII text outside a deprecated message: " + Q(v.S))
		}
	case List:
		for _, item := range v.L {
			ASCIIOnly(item, key, hasKey, depth+1)
		}
	case Map:
		for _, name := range v.M.Keys {
			if !IsASCII(name) {
				Reject("a non-ASCII key " + Q(name))
			}
			ASCIIOnly(v.M.Vals[name], name, true, depth+1)
		}
	}
}

// IsASCII says whether s holds only ASCII.
func IsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
