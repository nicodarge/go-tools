package golangciguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSuppressionIsRefused(t *testing.T) {
	for label, tc := range map[string]struct{ src, word string }{
		"line directive":                {"package p\n\n//nolint\nvar x = 1\n", "nolint"},
		"spaced":                        {"package p\n\n// nolint\nvar x = 1\n", "nolint"},
		"many spaces":                   {"package p\n\n//    nolint\nvar x = 1\n", "nolint"},
		"tab":                           {"package p\n\n//\tnolint\nvar x = 1\n", "nolint"},
		"with a linter":                 {"package p\n\n//nolint:gosec // reason\nvar x = 1\n", "nolint"},
		"upper case":                    {"package p\n\n// NOLINT\nvar x = 1\n", "nolint"},
		"mixed case":                    {"package p\n\n// NoLint:all\nvar x = 1\n", "nolint"},
		"trailing":                      {"package p\n\nvar x = 1 //nolint:unused\n", "nolint"},
		"in a function":                 {"package p\n\nfunc f() {\n\t_ = 1 // nolint\n}\n", "nolint"},
		"in the middle of a comment":    {"package p\n\n// we need nolint here\nvar x = 1\n", "nolint"},
		"block":                         {"package p\n\n/* nolint */\nvar x = 1\n", "nolint"},
		"block without spaces":          {"package p\n\n/*nolint*/\nvar x = 1\n", "nolint"},
		"block on several lines":        {"package p\n\n/*\n  nolint:gosec\n*/\nvar x = 1\n", "nolint"},
		"block trailing":                {"package p\n\nvar x = 1 /* nolint */\n", "nolint"},
		"doc comment":                   {"package p\n\n// Doc.\n//\n//nolint:revive\nvar x = 1\n", "nolint"},
		"before the package clause":     {"//nolint\npackage p\n", "nolint"},
		"prose naming a linter":         {"package p\n\n// nolintlint reports unused directives.\nvar x = 1\n", "nolint"},
		"nosec":                         {"package p\n\n// #nosec\nvar x = 1\n", "nosec"},
		"nosec without space":           {"package p\n\n//#nosec G304\nvar x = 1\n", "nosec"},
		"nosec with a rule":             {"package p\n\nvar x = 1 // #nosec G104 -- reason\n", "nosec"},
		"nosec spaced hash":             {"package p\n\n// # nosec\nvar x = 1\n", "nosec"},
		"nosec bare":                    {"package p\n\n// nosec\nvar x = 1\n", "nosec"},
		"nosec upper case":              {"package p\n\n// #NOSEC\nvar x = 1\n", "nosec"},
		"nosec block":                   {"package p\n\n/* #nosec */\nvar x = 1\n", "nosec"},
		"after a string holding a hash": {"package p\n\nvar x = \"#\" //nolint\n", "nolint"},
		"after a raw string":            {"package p\n\nvar x = `a // b` // nolint\n", "nolint"},
		"after a rune":                  {"package p\n\nvar x = '/' // nolint\n", "nolint"},
		"revive disable":                {"package p\n\n//revive:disable\nvar x = 1\n", "revive:"},
		"revive disable a rule":         {"package p\n\n//revive:disable:exported reason\nvar x = 1\n", "revive:"},
		"revive disable-line":           {"package p\n\nvar x = 1 //revive:disable-line:var-naming\n", "revive:"},
		"revive disable-next-line":      {"package p\n\n//revive:disable-next-line:exported\nvar x = 1\n", "revive:"},
		"revive enable":                 {"package p\n\n//revive:enable\nvar x = 1\n", "revive:"},
		"revive spaced":                 {"package p\n\n// revive:disable\nvar x = 1\n", "revive:"},
		"revive upper case":             {"package p\n\n// REVIVE:DISABLE\nvar x = 1\n", "revive:"},
		"revive block":                  {"package p\n\n/* revive:disable */\nvar x = 1\n", "revive:"},
		"exhaustive ignore":             {"package p\n\n//exhaustive:ignore\nvar x = 1\n", "exhaustive:"},
		"exhaustive enforce":            {"package p\n\n//exhaustive:enforce\nvar x = 1\n", "exhaustive:"},
		"exhaustive spaced":             {"package p\n\n// exhaustive:ignore\nvar x = 1\n", "exhaustive:"},
		"exhaustive trailing":           {"package p\n\nvar x = 1 //exhaustive:ignore\n", "exhaustive:"},
		"CRLF line endings":             {"package p\r\n\r\n//nolint\r\nvar x = 1\r\n", "nolint"},
	} {
		dir := t.TempDir()
		err := ScanGo(write(t, dir, "x.go", tc.src))
		if err == nil {
			t.Errorf("%s: accepted", label)
			continue
		}
		if want := fmt.Sprintf("a comment holds %q", tc.word); !strings.Contains(err.Error(), want) {
			t.Errorf("%s: refused for another reason: %v, want %q", label, err, want)
		}
	}
}

func TestCleanSourceIsAccepted(t *testing.T) {
	for label, src := range map[string]string{
		"plain":              "package p\n\n// Doc comment.\nvar x = 1\n",
		"block":              "package p\n\n/* a block\n   comment */\nvar x = 1\n",
		"string literal":     "package p\n\nvar x = \"//nolint\"\n",
		"raw string":         "package p\n\nvar x = `\n//nolint\n// #nosec\n`\n",
		"concatenation":      "package p\n\nvar x = \"no\" + \"lint\"\n",
		"identifier":         "package p\n\nvar nolintlint, nosec = 1, 2\n",
		"empty file":         "package p\n",
		"similar words":      "package p\n\n// no lint, no sec, no-lint, nos ec, revive, exhaustive, exhaustively\nvar x = 1\n",
		"revive in a string": "package p\n\nvar x = \"revive:disable\"\n",
		"a line directive":   "package p\n\n//line other.go:10\nvar x = 1\n",
		"a go:build header":  "//go:build linux\n\npackage p\n",
	} {
		dir := t.TempDir()
		if err := ScanGo(write(t, dir, "x.go", src)); err != nil {
			t.Errorf("%s: refused: %v", label, err)
		}
	}
}

func TestSuppressionIsReportedAtItsRealLine(t *testing.T) {
	dir := t.TempDir()
	err := ScanGo(write(t, dir, "x.go", "package p\n\n//line elsewhere.go:100\n//nolint\nvar x = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "x.go:4:1") {
		t.Errorf("the refusal does not name the line in the file: %v", err)
	}
}

func TestSourceThatCannotBeJudgedIsRefused(t *testing.T) {
	for label, src := range map[string]string{
		"an unterminated block comment": "package p\n\n/* open\nvar x = 1\n",
		"an unterminated string":        "package p\n\nvar x = \"open\n",
		"an unterminated raw string":    "package p\n\nvar x = `open\n",
		"an illegal character":          "package p\n\n# nosec\nvar x = 1\n",
		"a NUL byte":                    "package p\n\nvar x = 1 // \x00\n",
		"an invalid UTF-8 sequence":     "package p\n\nvar x = \"\xff\"\n",
		"a bad rune literal":            "package p\n\nvar x = ''\n",
	} {
		dir := t.TempDir()
		err := ScanGo(write(t, dir, "x.go", src))
		if err == nil {
			t.Errorf("%s: accepted", label)
		} else if !strings.Contains(err.Error(), "cannot tokenize") {
			t.Errorf("%s: refused for another reason: %v", label, err)
		}
	}
}

func TestSourceThatCannotBeReadIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := ScanGo(filepath.Join(dir, "missing.go")); err == nil {
		t.Error("a file that is not there is accepted")
	}
	if err := ScanGo(dir); err == nil {
		t.Error("a directory is accepted")
	}
	unreadable := write(t, dir, "locked.go", "package p\n")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if err := ScanGo(unreadable); err == nil {
			t.Error("a file that cannot be opened is accepted")
		}
	}

	target := write(t, dir, "real.go", "package p\n")
	link := filepath.Join(dir, "link.go")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := ScanGo(link); err == nil {
		t.Error("a symbolic link is accepted")
	}
}
