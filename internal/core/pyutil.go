package core

import (
	"fmt"
	"strconv"
	"strings"
)

// Reject is the refusal every checker ends in: it is raised with panic and recovered once, in run.
type Rejection struct{ Msg string }

// InternalError stands for a Python exception the checkers did not catch; the name is what
// `FAIL: internal error (<name>)` prints.
type InternalError struct{ Name string }

func Reject(msg string) { panic(Rejection{msg}) }

func Rejectf(format string, args ...any) { panic(Rejection{fmt.Sprintf(format, args...)}) }

func Crash(name string) { panic(InternalError{name}) }

// q makes an attacker-supplied string safe to print in one line: cut to limit characters,
// anything but printable ASCII replaced by `?`.
func Q(x any) string { return Qn(x, 60) }

func Qn(x any, limit int) string {
	var text string
	switch v := x.(type) {
	case string:
		text = v
	case *Value:
		text = PyStr(v)
	case int:
		text = strconv.Itoa(v)
	case nil:
		text = "None"
	default:
		text = fmt.Sprint(v)
	}
	var out strings.Builder
	n := 0
	for _, r := range text {
		if n >= limit {
			break
		}
		n++
		if r >= 0x20 && r <= 0x7e {
			out.WriteRune(r)
		} else {
			out.WriteByte('?')
		}
	}
	return out.String()
}

// pyRepr is Python's repr() of a parsed value, as far as a message can show it.
func PyRepr(v *Value) string {
	switch KindOf(v) {
	case Str:
		return pyStrRepr(v.S)
	case List:
		parts := make([]string, len(v.L))
		for i, item := range v.L {
			parts[i] = PyRepr(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case Map:
		parts := make([]string, 0, len(v.M.Keys))
		for _, k := range v.M.Keys {
			parts = append(parts, pyStrRepr(k)+": "+PyRepr(v.M.Vals[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return PyStr(v)
}

func pyStrRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString("\\n")
		case r == '\r':
			b.WriteString("\\r")
		case r == '\t':
			b.WriteString("\\t")
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\x%02x", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pyStr is Python's str() of a parsed value.
func PyStr(v *Value) string {
	switch KindOf(v) {
	case Null:
		return "None"
	case Bool:
		if v.B {
			return "True"
		}
		return "False"
	case Num:
		return v.S
	case Str:
		return v.S
	}
	return PyRepr(v)
}

// Guard runs fn and turns whatever it panics with into the line a checker prints: "" when fn
// returned, `FAIL: <reason>` for a refusal and `FAIL: internal error (<name>)` for anything else.
func Guard(fn func()) (line string) {
	defer func() {
		switch r := recover().(type) {
		case nil:
		case Rejection:
			line = "FAIL: " + r.Msg
		case InternalError:
			line = "FAIL: internal error (" + r.Name + ")"
		default:
			line = "FAIL: internal error (RuntimeError)"
		}
	}()
	fn()
	return ""
}
