package checksumcheck

import (
	"strings"
	"unicode/utf8"
)

// A reader for the one thing the checker takes from scripts/checksums.py: its top-level
// `TOOLS = (Tool(...), ...)` assignment, read as data. Python's ast.parse and ast.literal_eval are
// the original; this reads the same statement from the source text without running or importing
// anything. It splits the file into logical lines (strings, comments, brackets and continuation
// lines understood), finds the single-target `TOOLS = ...` assignments that start in column 0, and
// parses only that expression, down to literals. An expression it does not read as a literal is
// kept as `other`, which is "not a literal" exactly as ast.literal_eval treats it.
//
// What it does not do: check the syntax of the rest of the file. A file that Python would refuse
// elsewhere than in its TOOLS statement is read here. The mapping is taken from the base tip, a
// file that was already merged, which is why that is tolerable.

type pySyntax struct{ msg string }

func syntaxErr(msg string) { panic(pySyntax{msg}) }

type constKind uint8

const (
	cStr constKind = iota
	cBytes
	cInt
	cFloat
	cComplex
	cBool
	cNone
	cEllipsis
)

type nodeKind uint8

const (
	nConst nodeKind = iota
	nName
	nCall
	nTuple
	nList
	nDict
	nSet
	nOther
)

type pyNode struct {
	kind  nodeKind
	ckind constKind
	s     string // a string's value, a name
	fn    *pyNode
	args  []*pyNode
	kws   []pyKeyword
	elts  []*pyNode
	keys  []*pyNode // dict keys, nil for a ** entry
}

type pyKeyword struct {
	name  string // "" for **mapping
	value *pyNode
}

type pyParser struct {
	src   string
	pos   int
	depth int
}

// --------------------------------------------------------------------------- statements

// findTools returns the value of the last top-level single-target `TOOLS = ...` assignment, or nil.
func findTools(source string) (tools *pyNode) {
	src := strings.ReplaceAll(strings.ReplaceAll(source, "\r\n", "\n"), "\r", "\n")
	p := &pyParser{src: src}
	for p.pos < len(src) {
		start := p.pos
		first := src[start]
		isStatement := first != ' ' && first != '\t' && first != '\f' && first != '\n' && first != '#'
		if isStatement && first != '\\' {
			if node, ok := p.tryToolsAssign(start); ok {
				tools = node
			}
		}
		p.pos = start
		p.skipLogicalLine()
	}
	return tools
}

// skipLogicalLine advances past one logical line, strings and brackets included.
func (p *pyParser) skipLogicalLine() {
	depth := 0
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '#':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '\n':
			p.pos++
			if depth == 0 {
				return
			}
		case c == '\\' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '\n':
			p.pos += 2
		case c == '\'' || c == '"':
			p.skipStringBody()
		case c == '(' || c == '[' || c == '{':
			depth++
			p.pos++
		case c == ')' || c == ']' || c == '}':
			depth--
			if depth < 0 {
				syntaxErr("unmatched '" + string(c) + "'")
			}
			p.pos++
		default:
			p.pos++
		}
	}
	if depth > 0 {
		syntaxErr("a bracket was never closed")
	}
}

// skipStringBody skips a string whose opening quote is at p.pos.
func (p *pyParser) skipStringBody() {
	quote := p.src[p.pos]
	triple := strings.HasPrefix(p.src[p.pos:], strings.Repeat(string(quote), 3))
	if triple {
		p.pos += 3
	} else {
		p.pos++
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '\\':
			p.pos += 2
		case triple && strings.HasPrefix(p.src[p.pos:], strings.Repeat(string(quote), 3)):
			p.pos += 3
			return
		case !triple && c == quote:
			p.pos++
			return
		case !triple && c == '\n':
			syntaxErr("unterminated string literal")
		default:
			p.pos++
		}
	}
	syntaxErr("unterminated string literal")
}

func isIdentChar(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

// tryToolsAssign parses `TOOLS = <expr>` at start when it is a single-target assignment.
func (p *pyParser) tryToolsAssign(start int) (*pyNode, bool) {
	rest := p.src[start:]
	if !strings.HasPrefix(rest, "TOOLS") || (len(rest) > 5 && isIdentChar(rest[5])) {
		return nil, false
	}
	p.pos = start + 5
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
	if p.pos >= len(p.src) || p.src[p.pos] != '=' || (p.pos+1 < len(p.src) && p.src[p.pos+1] == '=') {
		return nil, false
	}
	p.pos++
	node := p.exprList()
	p.skipSpace()
	if p.pos < len(p.src) && p.src[p.pos] == '=' {
		return nil, false // TOOLS = x = ... has two targets
	}
	if p.pos < len(p.src) && p.src[p.pos] != '\n' && p.src[p.pos] != ';' {
		syntaxErr("invalid syntax")
	}
	return node, true
}

// --------------------------------------------------------------------------- expressions

// skipSpace skips blanks, comments and continuations; inside brackets also newlines.
func (p *pyParser) skipSpace() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\f':
			p.pos++
		case c == '#':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '\\' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '\n':
			p.pos += 2
		case c == '\n' && p.depth > 0:
			p.pos++
		default:
			return
		}
	}
}

func (p *pyParser) peekByte() byte {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

// exprList is an expression, or an unparenthesised tuple of them.
func (p *pyParser) exprList() *pyNode {
	first := p.expr(false)
	if p.peekByte() != ',' {
		return first
	}
	tuple := &pyNode{kind: nTuple, elts: []*pyNode{first}}
	for p.peekByte() == ',' {
		p.pos++
		if c := p.peekByte(); c == 0 || c == '\n' || c == ';' || c == '=' {
			break
		}
		tuple.elts = append(tuple.elts, p.expr(false))
	}
	return tuple
}

func isDelimiter(c byte, colon bool) bool {
	switch c {
	case 0, ',', ')', ']', '}', ';', '\n', '=':
		return true
	case ':':
		return colon
	}
	return false
}

// opaque skips the rest of an expression this does not read, to the delimiter that ends it.
func (p *pyParser) opaque(colon bool) *pyNode {
	depth := 0
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '#':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
			continue
		case c == '\\' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '\n':
			p.pos += 2
			continue
		case c == '\n':
			if depth == 0 && p.depth == 0 {
				return &pyNode{kind: nOther}
			}
			p.pos++
			continue
		case c == '\'' || c == '"':
			p.skipStringBody()
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				return &pyNode{kind: nOther}
			}
			depth--
		case depth == 0 && (c == ',' || c == ';' || (c == ':' && colon)):
			return &pyNode{kind: nOther}
		case depth == 0 && c == '=' && (p.pos+1 >= len(p.src) || p.src[p.pos+1] != '=') &&
			(p.pos == 0 || !strings.ContainsRune("=!<>:", rune(p.src[p.pos-1]))):
			return &pyNode{kind: nOther}
		}
		p.pos++
	}
	return &pyNode{kind: nOther}
}

// expr parses one expression; colon says a `:` ends it (a dict key).
func (p *pyParser) expr(colon bool) *pyNode {
	node := p.atom()
	c := p.peekByte()
	if isDelimiter(c, colon) {
		return node
	}
	// An operator, a keyword (`if`, `for`, `and` ...), an attribute or a subscript: not a literal.
	word := p.peekWord()
	node = p.opaque(colon)
	if word == "for" || word == "async" {
		node.s = "comprehension"
	}
	return node
}

func (p *pyParser) peekWord() string {
	p.skipSpace()
	end := p.pos
	for end < len(p.src) && isIdentChar(p.src[end]) {
		end++
	}
	return p.src[p.pos:end]
}

func (p *pyParser) atom() *pyNode {
	c := p.peekByte()
	switch {
	case c == '-' || c == '+':
		p.pos++
		inner := p.atom()
		if inner.kind == nConst && (inner.ckind == cInt || inner.ckind == cFloat || inner.ckind == cComplex) {
			return inner
		}
		return &pyNode{kind: nOther}
	case c == '(':
		return p.bracketed(')', nTuple)
	case c == '[':
		return p.bracketed(']', nList)
	case c == '{':
		return p.braces()
	case c == '\'' || c == '"':
		return p.stringAtom()
	case c >= '0' && c <= '9' || (c == '.' && p.pos+1 < len(p.src) && p.src[p.pos+1] >= '0' && p.src[p.pos+1] <= '9'):
		return p.number()
	case c == '.':
		if strings.HasPrefix(p.src[p.pos:], "...") {
			p.pos += 3
			return &pyNode{kind: nConst, ckind: cEllipsis}
		}
		syntaxErr("invalid syntax")
	case c == '*':
		p.pos++
		p.opaque(false)
		return &pyNode{kind: nOther}
	case isIdentChar(c):
		return p.identOrString()
	}
	syntaxErr("invalid syntax")
	return nil
}

func (p *pyParser) identOrString() *pyNode {
	start := p.pos
	for p.pos < len(p.src) && isIdentChar(p.src[p.pos]) {
		p.pos++
	}
	word := p.src[start:p.pos]
	// A string prefix: r, b, u, f and the pairs, directly before a quote.
	if p.pos < len(p.src) && (p.src[p.pos] == '\'' || p.src[p.pos] == '"') && isStringPrefix(word) {
		p.pos = start
		return p.stringAtom()
	}
	switch word {
	case "True", "False":
		return &pyNode{kind: nConst, ckind: cBool}
	case "None":
		return &pyNode{kind: nConst, ckind: cNone}
	case "lambda", "not", "await", "yield", "async":
		p.pos = start
		return p.opaque(false)
	}
	node := &pyNode{kind: nName, s: word}
	return p.maybeCall(node)
}

func (p *pyParser) maybeCall(fn *pyNode) *pyNode {
	if p.peekByte() == '(' {
		return p.call(fn)
	}
	return fn
}

func isStringPrefix(w string) bool {
	switch strings.ToLower(w) {
	case "r", "u", "b", "f", "br", "rb", "fr", "rf":
		return true
	}
	return false
}

func (p *pyParser) call(fn *pyNode) *pyNode {
	p.pos++ // (
	p.depth++
	call := &pyNode{kind: nCall, fn: fn}
	for {
		c := p.peekByte()
		if c == ')' {
			p.pos++
			break
		}
		if c == 0 {
			syntaxErr("'(' was never closed")
		}
		switch {
		case strings.HasPrefix(p.src[p.pos:], "**"):
			p.pos += 2
			call.kws = append(call.kws, pyKeyword{"", p.expr(false)})
		case c == '*':
			p.pos++
			p.expr(false)
			call.args = append(call.args, &pyNode{kind: nOther})
		default:
			arg := p.expr(false)
			if arg.kind == nName && p.peekByte() == '=' && !strings.HasPrefix(p.src[p.pos:], "==") {
				p.pos++
				call.kws = append(call.kws, pyKeyword{arg.s, p.expr(false)})
			} else {
				call.args = append(call.args, arg)
			}
		}
		switch p.peekByte() {
		case ',':
			p.pos++
		case ')':
		default:
			syntaxErr("invalid syntax")
		}
	}
	p.depth--
	if c := p.peekByte(); c == '.' || c == '[' || c == '(' {
		return p.opaque(false)
	}
	return call
}

// bracketed reads a parenthesised expression, a tuple or a list.
func (p *pyParser) bracketed(closer byte, kind nodeKind) *pyNode {
	p.pos++
	p.depth++
	node := &pyNode{kind: kind}
	sawComma := false
	for {
		c := p.peekByte()
		if c == closer {
			p.pos++
			break
		}
		if c == 0 {
			syntaxErr("a bracket was never closed")
		}
		elt := p.expr(false)
		node.elts = append(node.elts, elt)
		switch p.peekByte() {
		case ',':
			sawComma = true
			p.pos++
		case closer:
		default:
			syntaxErr("invalid syntax")
		}
	}
	p.depth--
	if kind == nTuple && !sawComma && len(node.elts) == 1 {
		return node.elts[0] // (x) is x
	}
	for _, e := range node.elts {
		if e.kind == nOther && e.s == "comprehension" {
			return &pyNode{kind: nOther}
		}
	}
	return node
}

// braces reads a dict or a set display.
func (p *pyParser) braces() *pyNode {
	p.pos++
	p.depth++
	node := &pyNode{kind: nSet}
	isDict := false
	for {
		c := p.peekByte()
		if c == '}' {
			p.pos++
			break
		}
		if c == 0 {
			syntaxErr("'{' was never closed")
		}
		if strings.HasPrefix(p.src[p.pos:], "**") {
			p.pos += 2
			isDict = true
			p.expr(false)
			node.keys = append(node.keys, nil)
			node.elts = append(node.elts, &pyNode{kind: nOther})
		} else {
			key := p.expr(true)
			if p.peekByte() == ':' {
				p.pos++
				isDict = true
				node.keys = append(node.keys, key)
				node.elts = append(node.elts, p.expr(false))
			} else {
				node.elts = append(node.elts, key)
			}
		}
		switch p.peekByte() {
		case ',':
			p.pos++
		case '}':
		default:
			syntaxErr("invalid syntax")
		}
	}
	p.depth--
	for _, e := range node.elts {
		if e.kind == nOther && e.s == "comprehension" {
			return &pyNode{kind: nOther}
		}
	}
	if len(node.elts) == 0 {
		return &pyNode{kind: nDict}
	}
	if isDict {
		node.kind = nDict
	}
	return node
}

func (p *pyParser) number() *pyNode {
	start := p.pos
	for p.pos < len(p.src) && (isIdentChar(p.src[p.pos]) || p.src[p.pos] == '.' ||
		((p.src[p.pos] == '+' || p.src[p.pos] == '-') && (p.src[p.pos-1] == 'e' || p.src[p.pos-1] == 'E') &&
			!strings.HasPrefix(strings.ToLower(p.src[start:]), "0x"))) {
		p.pos++
	}
	text := strings.ToLower(p.src[start:p.pos])
	kind := cInt
	switch {
	case strings.HasSuffix(text, "j"):
		kind = cComplex
	case strings.HasPrefix(text, "0x"), strings.HasPrefix(text, "0b"), strings.HasPrefix(text, "0o"):
	case strings.ContainsAny(text, ".e"):
		kind = cFloat
	}
	return &pyNode{kind: nConst, ckind: kind}
}

// stringAtom reads one or more adjacent string literals as one constant.
func (p *pyParser) stringAtom() *pyNode {
	var out strings.Builder
	node := &pyNode{kind: nConst, ckind: cStr}
	first := true
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			break
		}
		start := p.pos
		for p.pos < len(p.src) && isIdentChar(p.src[p.pos]) {
			p.pos++
		}
		prefix := strings.ToLower(p.src[start:p.pos])
		if p.pos >= len(p.src) || (p.src[p.pos] != '\'' && p.src[p.pos] != '"') || (prefix != "" && !isStringPrefix(prefix)) {
			p.pos = start
			break
		}
		raw := strings.Contains(prefix, "r")
		quote := p.src[p.pos]
		triple := strings.HasPrefix(p.src[p.pos:], strings.Repeat(string(quote), 3))
		bodyStart := p.pos + 1
		if triple {
			bodyStart = p.pos + 3
		}
		p.skipStringBody()
		bodyEnd := p.pos - 1
		if triple {
			bodyEnd = p.pos - 3
		}
		body := p.src[bodyStart:bodyEnd]
		switch {
		case strings.Contains(prefix, "f"):
			return p.finishOpaqueString()
		case strings.Contains(prefix, "b"):
			if first {
				node.ckind = cBytes
			}
		}
		if raw || strings.Contains(prefix, "b") {
			out.WriteString(body)
		} else {
			out.WriteString(unescape(body))
		}
		first = false
	}
	node.s = out.String()
	return node
}

// finishOpaqueString: an f-string is not a literal; whatever adjacent strings follow are part of it.
func (p *pyParser) finishOpaqueString() *pyNode {
	for {
		p.skipSpace()
		save := p.pos
		for p.pos < len(p.src) && isIdentChar(p.src[p.pos]) {
			p.pos++
		}
		if p.pos < len(p.src) && (p.src[p.pos] == '\'' || p.src[p.pos] == '"') {
			p.skipStringBody()
			continue
		}
		p.pos = save
		return &pyNode{kind: nOther}
	}
}

func unescape(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch e := s[i]; e {
		case '\n':
		case '\\', '\'', '"':
			b.WriteByte(e)
		case 'a':
			b.WriteByte(7)
		case 'b':
			b.WriteByte(8)
		case 'f':
			b.WriteByte(12)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte(11)
		case 'x', 'u', 'U':
			n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
			if i+n < len(s) {
				v, ok := parseHex(s[i+1 : i+1+n])
				if ok {
					b.WriteRune(rune(v))
					i += n
					continue
				}
			}
			b.WriteByte('\\')
			b.WriteByte(e)
		case 'N':
			if end := strings.IndexByte(s[i:], '}'); i+1 < len(s) && s[i+1] == '{' && end > 0 {
				b.WriteRune(utf8.RuneError)
				i += end
				continue
			}
			b.WriteString("\\N")
		default:
			if e >= '0' && e <= '7' {
				v, n := 0, 0
				for n < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7' {
					v = v*8 + int(s[i]-'0')
					i++
					n++
				}
				i--
				b.WriteRune(rune(v))
				continue
			}
			b.WriteByte('\\')
			b.WriteByte(e)
		}
	}
	return b.String()
}

func parseHex(s string) (int, bool) {
	v := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			v = v*16 + int(c-'0')
		case c >= 'a' && c <= 'f':
			v = v*16 + int(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v*16 + int(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, v <= utf8.MaxRune
}
