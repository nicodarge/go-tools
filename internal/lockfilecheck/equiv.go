//go:build equiv

package lockfilecheck

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/nicodarge/go-tools/internal/core"
)

// The equivalence build only: the hooks that let an external test suite, which patches module
// globals and calls functions in-process, drive this binary. Never part of the shipped build.

func init() {
	equivHook = func() {
		if v := os.Getenv("EQUIV_MAX_BYTES"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				core.Crash("ValueError")
			}
			maxLockfileBytes = n
		}
		if v := os.Getenv("EQUIV_CRASH"); v != "" {
			core.Crash(v)
		}
	}
}

// EquivSatisfies prints whether args[0] is in the range args[1], or the refusal.
func EquivSatisfies(args []string, out io.Writer) int {
	if len(args) != 2 {
		return report(out, "FAIL: usage", 1)
	}
	res := ""
	fail := core.Guard(func() {
		if satisfiesStr(args[0], args[1]) {
			res = "true"
		} else {
			res = "false"
		}
	})
	if fail != "" {
		res = fail
	}
	return report(out, res, 0)
}

// report writes the answer and returns code, or 1 when the answer could not be written.
func report(out io.Writer, line string, code int) int {
	if say(out, line) != nil {
		return 1
	}
	return code
}

// piecewise hands a file out a few bytes at a time, as a network answer arrives.
type piecewise struct {
	r io.Reader
	n int
}

func (p *piecewise) Read(b []byte) (int, error) {
	if len(b) > p.n {
		b = b[:p.n]
	}
	return p.r.Read(b)
}

// EquivPackument reads the packument in file args[0] as the registry client reads one of the name
// args[1], within args[2] bytes and members of args[3] characters, args[4] bytes at a time, and
// prints what was read or the refusal.
func EquivPackument(args []string, out io.Writer) int {
	if len(args) != 5 {
		return report(out, "FAIL: usage", 1)
	}
	limit, limitErr := strconv.ParseInt(args[2], 10, 64)
	memberCap, capErr := strconv.ParseInt(args[3], 10, 64)
	pieceSize, pieceErr := strconv.Atoi(args[4])
	if limitErr != nil || capErr != nil || pieceErr != nil {
		return report(out, "FAIL: usage", 1)
	}
	core.SetMemberCap(memberCap)
	f, err := core.OpenFile(args[0])
	if err != nil {
		return report(out, "FAIL: no such file", 1)
	}
	defer func() { _ = f.Close() }()
	res := ""
	fail := core.Guard(func() {
		p, readErr := core.ParsePackument(&piecewise{f, pieceSize}, args[1], limit)
		if readErr != nil {
			core.Reject("read error")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "name=%s", describe(p.Name))
		fmt.Fprintf(&b, " time=%s", describe(p.Time))
		fmt.Fprintf(&b, " versions=%v", p.HasVers)
		for _, v := range p.Versions {
			fmt.Fprintf(&b, " [%s %v %v]", core.Q(v.Name), v.IsObject, v.Deprecated)
		}
		res = b.String()
	})
	if fail != "" {
		res = fail
	}
	return report(out, res, 0)
}

// describe says what kind of value was read, and a string's text.
func describe(v *core.Value) string {
	if v == nil {
		return "absent"
	}
	switch v.K {
	case core.Str:
		return "str:" + core.Q(v.S)
	case core.Map:
		return fmt.Sprintf("map%d", v.M.Len())
	case core.List:
		return fmt.Sprintf("list%d", len(v.L))
	case core.Num:
		return "num"
	case core.Bool:
		return "bool"
	}
	return "null"
}
