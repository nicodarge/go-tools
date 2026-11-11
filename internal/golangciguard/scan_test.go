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
		"plain":                       "package p\n\n// Doc comment.\nvar x = 1\n",
		"block":                       "package p\n\n/* a block\n   comment */\nvar x = 1\n",
		"string literal":              "package p\n\nvar x = \"//nolint\"\n",
		"raw string":                  "package p\n\nvar x = `\n//nolint\n// #nosec\n`\n",
		"concatenation":               "package p\n\nvar x = \"no\" + \"lint\"\n",
		"identifier":                  "package p\n\nvar nolintlint, nosec = 1, 2\n",
		"empty file":                  "package p\n",
		"similar words":               "package p\n\n// no lint, no sec, no-lint, nos ec, revive, exhaustive, exhaustively\nvar x = 1\n",
		"revive in a string":          "package p\n\nvar x = \"revive:disable\"\n",
		"a directive in a string":     "package p\n\nvar x = \"//lint:ignore U1000 reason\"\n",
		"a directive in a raw string": "package p\n\nvar x = `\n//go:linkname a b\n//revive:disable\n`\n",
		"prose with a colon":          "package p\n\n// prose: with colon\nvar x = 1\n",
		"a link":                      "package p\n\n// see https://x\nvar x = 1\n",
		"revive in prose":             "package p\n\n// revive is a linter, see revive.run\nvar x = 1\n",
		"a note naming revive":        "package p\n\n// Note: revive reads its rules from the config\nvar x = 1\n",
		"a created-by line, mid-line": "package p\n\nvar x = \"a // Created by cgo - DO NOT EDIT\"\n",
		"a spaced lint ignore":        "package p\n\n// lint:ignore U1000 reason\nvar x = 1\n",
		"a colon after a word":        "package p\n\n// Note: gocyclo:ignore is not read with a space\nvar x = 1\n",
		"a go:build header":           "//go:build linux\n\npackage p\n",
		"go:build and a tag":          "//go:build linux && gc\n\npackage p\n",
		"a go:generate":               "package p\n\n//go:generate stringer -type=T\ntype T int\n",
		"a bare go:generate":          "package p\n\n//go:generate\ntype T int\n",
		"a go:embed":                  "package p\n\nimport _ \"embed\"\n\n//go:embed f\nvar s string\n",
		"a plus-build line":           "// +build linux\n\npackage p\n",
		"generated, mid-line":         "package p\n\nvar x = \"a // Code generated by hand. DO NOT EDIT.\"\n",
		"a spaced line comment":       "package p\n\n// line other.go:10\nvar x = 1\n",
		"line in the middle":          "package p\n\n// the //line form is a directive\nvar x = 1\n",
		"line in a string":            "package p\n\nvar x = \"//line other.go:10\"\n",
		"line in a raw string":        "package p\n\nvar x = `\n//line other.go:10\n`\n",
	} {
		dir := t.TempDir()
		if err := ScanGo(write(t, dir, "x.go", src)); err != nil {
			t.Errorf("%s: refused: %v", label, err)
		}
	}
}

func TestDirectiveShapeIsRefused(t *testing.T) {
	for label, src := range map[string]string{
		"revive disable":           "package p\n\n//revive:disable\nvar x = 1\n",
		"revive disable a rule":    "package p\n\n//revive:disable:exported reason\nvar x = 1\n",
		"revive disable-line":      "package p\n\nvar x = 1 //revive:disable-line:var-naming\n",
		"revive disable-next-line": "package p\n\n//revive:disable-next-line:exported\nvar x = 1\n",
		"revive enable":            "package p\n\n//revive:enable\nvar x = 1\n",
		"revive upper case":        "package p\n\n//REVIVE:DISABLE\nvar x = 1\n",
		"revive block":             "package p\n\n/*revive:disable*/\nvar x = 1\n",
		"exhaustive ignore":        "package p\n\n//exhaustive:ignore\nvar x = 1\n",
		"exhaustive enforce":       "package p\n\n//exhaustive:enforce\nvar x = 1\n",
		"exhaustive trailing":      "package p\n\nvar x = 1 //exhaustive:ignore\n",
		"gosec disable":            "package p\n\n//gosec:disable G304\nvar x = 1\n",
		"gocyclo ignore":           "package p\n\n//gocyclo:ignore\nfunc f() {}\n",
		"gocognit ignore":          "package p\n\n//gocognit:ignore\nfunc f() {}\n",
		"iface ignore":             "package p\n\n//iface:ignore\ntype I interface{}\n",
		"iface ignore a rule":      "package p\n\n//iface:ignore=unused\ntype I interface{}\n",
		"exhaustruct ignore":       "package p\n\nvar x = struct{ A, B int }{A: 1} //exhaustruct:ignore\n",
		"exhaustruct block":        "package p\n\nvar x = struct{ A, B int }{A: 1} /*exhaustruct:ignore*/\n",
		"godoclint disable":        "package p\n\n//godoclint:disable\nvar x = 1\n",
		"forbidigo permit":         "package p\n\nvar x = 1 //permit:fmt.Println\n",
		"staticcheck lint ignore":  "package p\n\n//lint:ignore U1000 reason\nfunc unusedFn() {}\n",
		"staticcheck file-ignore":  "//lint:file-ignore U1000 reason\npackage p\n",
		"staticcheck, upper case":  "package p\n\n//LINT:IGNORE U1000 reason\nfunc unusedFn() {}\n",
		"go:linkname":              "package p\n\nimport _ \"unsafe\"\n\n//go:linkname unusedFn probe.hidden\nfunc unusedFn() {}\n",
		"go:noinline":              "package p\n\n//go:noinline\nfunc f() {}\n",
		"go:nosplit":               "package p\n\n//go:nosplit\nfunc f() {}\n",
		"go:buildx":                "//go:buildx linux\n\npackage p\n",
		"go:build with a colon":    "//go:build:x\n\npackage p\n",
		"go:generatex":             "package p\n\n//go:generatex\nvar x = 1\n",
		"upper case go:build":      "//GO:BUILD linux\n\npackage p\n",
		"go:build in a block":      "/*go:build linux*/\n\npackage p\n",
		"a link without a space":   "package p\n\n//https://x\nvar x = 1\n",
		"a TODO without a space":   "package p\n\n//TODO: later\nvar x = 1\n",
		"a dotted name":            "package p\n\n//my.tool-x_1:run\nvar x = 1\n",
		"a directive after code":   "package p\n\nvar x = 1 //tool:off\n",
		"in a function":            "package p\n\nfunc f() {\n\t//tool:off\n}\n",
		"CRLF line endings":        "package p\r\n\r\n//tool:off\r\nvar x = 1\r\n",
	} {
		t.Run(label, func(t *testing.T) {
			err := ScanGo(write(t, t.TempDir(), "x.go", src))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), "a comment is written like a tool directive") {
				t.Errorf("refused for another reason: %v", err)
			}
		})
	}
}

func TestGeneratedHeaderIsRefused(t *testing.T) {
	for label, tc := range map[string]struct{ src, line string }{
		"a header":                           {"// Code generated by hand. DO NOT EDIT.\n\npackage p\n", "x.go:1:"},
		"a header after the package":         {"package p\n\n// Code generated by hand. DO NOT EDIT.\nvar x = 1\n", "x.go:3:"},
		"in a raw string":                    {"package p\n\nconst banner = `\n// Code generated by hand. DO NOT EDIT.\n`\n", "x.go:4:"},
		"without the suffix":                 {"package p\n\n// Code generated by hand.\nvar x = 1\n", "x.go:3:"},
		"with nothing after":                 {"package p\n\n// Code generated \nvar x = 1\n", "x.go:3:"},
		"indented":                           {"package p\n\nfunc f() {\n\t// Code generated by hand. DO NOT EDIT.\n}\n", "x.go:4:"},
		"indented by spaces":                 {"package p\n\nfunc f() {\n    // Code generated by hand. DO NOT EDIT.\n}\n", "x.go:4:"},
		"CRLF line endings":                  {"package p\r\n\r\n// Code generated by hand. DO NOT EDIT.\r\nvar x = 1\r\n", "x.go:3:"},
		"in a file that cannot be tokenized": {"package p\n\n// Code generated by hand. DO NOT EDIT.\nvar x = \"open\n", "x.go:3:"},
	} {
		t.Run(label, func(t *testing.T) {
			err := ScanGo(write(t, t.TempDir(), "x.go", tc.src))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.line) || !strings.Contains(err.Error(), "a line starts with") {
				t.Errorf("refused for another reason: %v", err)
			}
		})
	}
}

func TestReviveDirectiveIsRefused(t *testing.T) {
	for label, src := range map[string]string{
		"a space":            "package p\n\n// revive:disable\nvar x = 1\n",
		"many spaces":        "package p\n\n//   revive:disable:exported\nvar x = 1\n",
		"a tab":              "package p\n\n//\trevive:disable\nvar x = 1\n",
		"a form feed":        "package p\n\n//\frevive:disable\nvar x = 1\n",
		"mixed white space":  "package p\n\n// \t\f revive:enable\nvar x = 1\n",
		"upper case":         "package p\n\n// REVIVE:DISABLE\nvar x = 1\n",
		"disable-next-line":  "package p\n\n// revive:disable-next-line:exported\nvar x = 1\n",
		"after code":         "package p\n\nvar x = 1 // revive:disable-line:var-naming\n",
		"in a function":      "package p\n\nfunc f() {\n\t// revive:disable\n}\n",
		"a block":            "package p\n\n/* revive:disable */\nvar x = 1\n",
		"a block with a tab": "package p\n\n/*\trevive:disable */\nvar x = 1\n",
		"CRLF line endings":  "package p\r\n\r\n// revive:disable\r\nvar x = 1\r\n",
		"with rules listed":  "package p\n\n// revive:disable:exported,unused-parameter\nfunc F(a int) {}\n",
	} {
		t.Run(label, func(t *testing.T) {
			err := ScanGo(write(t, t.TempDir(), "x.go", src))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), "a comment is a revive directive") {
				t.Errorf("refused for another reason: %v", err)
			}
		})
	}
}

func TestGeneratedMarkerInACommentIsRefused(t *testing.T) {
	for label, src := range map[string]string{
		"the marker with a semicolon":     "// Code generated by x; DO NOT EDIT.\n\npackage p\n",
		"no space after the marker":       "//Code generated by x DO NOT EDIT.\n// Package p is documented.\npackage p\n",
		"a block comment":                 "/* Code generated by x DO NOT EDIT. */\n// Package p is documented.\npackage p\n",
		"a block with only the words":     "/* Code generated */\npackage p\n",
		"in a package comment":            "// Package p is documented. DO NOT EDIT\npackage p\n",
		"in a package comment, generated": "// Package p is documented, not Code generated.\npackage p\n",
		"a file was generated":            "// This file was generated by x. DO NOT EDIT\n// Package p is documented.\npackage p\n",
		"only the edit words":             "// DO NOT EDIT\n// Package p is documented.\npackage p\n",
		"lower case":                      "// code generated by x do not edit.\npackage p\n",
		"after code":                      "package p\n\nvar x = 1 // Code generated by nobody\n",
		"after the package clause":        "package p\n\n// Code generated\nvar x = 1\n",
		"no space, mid-file":              "package p\n\n//Code generated by hand. DO NOT EDIT.\nvar x = 1\n",
		"a block in a function":           "package p\n\nfunc f() {\n\t/* DO NOT EDIT */\n}\n",
		"CRLF line endings":               "// DO NOT EDIT\r\npackage p\r\n",
	} {
		t.Run(label, func(t *testing.T) {
			err := ScanGo(write(t, t.TempDir(), "x.go", src))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), "a marker that makes linters skip the file as generated") &&
				!strings.Contains(err.Error(), "a line starts with") {
				t.Errorf("refused for another reason: %v", err)
			}
		})
	}
}

func TestCgoMarkLineIsRefused(t *testing.T) {
	for label, src := range map[string]string{
		"in a raw string":         "package p\n\nvar x = `\n// Created by cgo - DO NOT EDIT\n`\n",
		"indented in a string":    "package p\n\nvar x = `\n\t// Created by cgo - DO NOT EDIT\n`\n",
		"CRLF in a raw string":    "package p\r\n\r\nvar x = `\r\n// Created by cgo - DO NOT EDIT\r\n`\r\n",
		"as a comment":            "package p\n\n// Created by cgo - DO NOT EDIT\n",
		"in an unterminated file": "package p\n\n// Created by cgo - DO NOT EDIT\nvar x = \"open\n",
	} {
		t.Run(label, func(t *testing.T) {
			err := ScanGo(write(t, t.TempDir(), "x.go", src))
			if err == nil {
				t.Fatal("accepted")
			}
		})
	}
	err := ScanGo(write(t, t.TempDir(), "x.go", "package p\n\nvar x = `\n// Created by cgo - DO NOT EDIT\n`\n"))
	if err == nil || !strings.Contains(err.Error(), "x.go:4:") || !strings.Contains(err.Error(), "the mark that makes linters skip cgo output") {
		t.Errorf("the raw-line check does not name the line: %v", err)
	}
}

func TestLineDirectiveIsRefused(t *testing.T) {
	for label, tc := range map[string]struct{ src, want string }{
		"a line directive":            {"package p\n\n//line other.go:10\nvar x = 1\n", `"//line"`},
		"the cgo file name":           {"package p\n\n//line _cgo_gotypes.go:1\nvar x = 1\n", `"//line"`},
		"a column":                    {"package p\n\n//line other.go:10:2\nvar x = 1\n", `"//line"`},
		"a bare directive":            {"package p\n\n//line\nvar x = 1\n", `"//line"`},
		"upper case":                  {"package p\n\n//LINE other.go:10\nvar x = 1\n", `"//line"`},
		"mixed case":                  {"package p\n\n//Line other.go:10\nvar x = 1\n", `"//line"`},
		"a block directive":           {"package p\n\nvar x /*line other.go:10*/ = 1\n", `"/*line"`},
		"a block directive, upper":    {"package p\n\nvar x /*LINE other.go:10*/ = 1\n", `"/*line"`},
		"a block directive, own line": {"package p\n\n/*line other.go:10*/\nvar x = 1\n", `"/*line"`},
		"indented":                    {"package p\n\nfunc f() {\n\t//line other.go:10\n}\n", `"//line"`},
		"after code":                  {"package p\n\nvar x = 1 //line other.go:10\n", `"//line"`},
		"before the package clause":   {"//line other.go:10\npackage p\n", `"//line"`},
		"in a doc comment":            {"package p\n\n// Doc.\n//line other.go:10\nvar x = 1\n", `"//line"`},
		"CRLF line endings":           {"package p\r\n\r\n//line other.go:10\r\nvar x = 1\r\n", `"//line"`},
	} {
		t.Run(label, func(t *testing.T) {
			err := ScanGo(write(t, t.TempDir(), "x.go", tc.src))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), "a comment starts with "+tc.want) {
				t.Errorf("refused for another reason: %v", err)
			}
		})
	}
}

func TestRefusalNamesTheRealLine(t *testing.T) {
	dir := t.TempDir()
	err := ScanGo(write(t, dir, "x.go", "package p\n\n\n//line elsewhere.go:100\nvar x = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "x.go:4:1") {
		t.Errorf("the refusal does not name the line in the file: %v", err)
	}
	err = ScanGo(write(t, dir, "y.go", "package p\n\nvar x = 1\n\n// nolint\nvar y = 1\n"))
	if err == nil || !strings.Contains(err.Error(), "y.go:5:1") {
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
