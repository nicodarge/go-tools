package golangciguard

import (
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// suppressionWords are the strings with which a comment of any shape suppresses a finding, those
// of golangci-lint itself and of gosec.
var suppressionWords = []string{"nolint", "nosec"}

// lineDirectives are the prefixes of a line directive: golangci-lint's cgo processor maps a position
// that a //line directive puts in a file named like cgo output (_cgo_gotypes.go) away, and drops
// the finding.
var lineDirectives = []string{"//line", "/*line"}

// directiveShape matches a comment that is written like a tool directive, `//name:` or `/*name:`
// with no space after the marker (//lint:ignore, //go:linkname, //exhaustive:ignore ...). Linters
// read their directives in this shape, so the shape is refused and a name is not looked up in a
// table of linters. revive is the exception, see reviveDirective.
var directiveShape = regexp.MustCompile(`^(?://|/\*)[A-Za-z][A-Za-z0-9_.-]*:`)

// reviveDirective matches the comment revive reads as a directive: its pattern allows white space
// (a space, a tab, a form feed) between the marker and the word, so the spaced form silences it too.
var reviveDirective = regexp.MustCompile(`(?i)^(?://|/\*)\s*revive:`)

// allowedDirectives are the directives that change no lint outcome. They must be followed by a
// space, a tab or the end of the comment.
var allowedDirectives = []string{"//go:build", "//go:generate", "//go:embed"}

// generatedPrefix starts the line that revive, staticcheck, unused and ineffassign read as the
// header of a generated file, which they then skip. They look at a comment (go/ast.IsGenerated) or
// at a line (revive), so the guard looks at every line of the raw bytes, a string literal included.
const generatedPrefix = "// Code generated "

// cgoLine is the line that staticcheck and unused take, anywhere in the raw bytes, for the mark of
// cgo output.
const cgoLine = "// Created by cgo - DO NOT EDIT"

// generatedMarkers are the strings that unparam looks for in the first comment group of a file to
// skip it as generated; the guard refuses them in any comment, in any case.
var generatedMarkers = []string{"Code generated", "DO NOT EDIT"}

// ScanGo returns an error when the Go source at path cannot be read or tokenized, has a line that
// starts (after white space) with generatedPrefix or is cgoLine wherever it is, or has a comment
// (line or block) that holds one of suppressionWords or generatedMarkers in any case and any
// spacing, that starts with one of lineDirectives in any case and at any column, that is a
// reviveDirective, or that has the directiveShape without being one of allowedDirectives. String
// and rune literals are not comments and are not looked at for anything but the generated header.
func ScanGo(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	src, err := readLimited(root, filepath.Base(path), maxGoBytes)
	if err != nil {
		return err
	}

	if err = checkGeneratedHeader(path, src); err != nil {
		return err
	}

	fset := token.NewFileSet()
	file := fset.AddFile(path, fset.Base(), len(src))
	var tokenErr error
	var s scanner.Scanner
	s.Init(file, src, func(pos token.Position, msg string) {
		if tokenErr == nil {
			tokenErr = fmt.Errorf("%s: %s", pos, msg)
		}
	}, scanner.ScanComments)
	for {
		pos, tok, lit := s.Scan()
		if tokenErr != nil {
			return fmt.Errorf("cannot tokenize: %w", tokenErr)
		}
		if tok == token.EOF {
			return nil
		}
		if tok != token.COMMENT {
			continue
		}
		position := file.PositionFor(pos, false)
		lower := strings.ToLower(lit)
		for _, prefix := range lineDirectives {
			if strings.HasPrefix(lower, prefix) {
				return fmt.Errorf("%s: a comment starts with %q, a line directive that moves findings to another file", position, prefix)
			}
		}
		for _, word := range suppressionWords {
			if strings.Contains(lower, word) {
				return fmt.Errorf("%s: a comment holds %q", position, word)
			}
		}
		for _, marker := range generatedMarkers {
			if strings.Contains(lower, strings.ToLower(marker)) {
				return fmt.Errorf("%s: a comment holds %q, a marker that makes linters skip the file as generated", position, marker)
			}
		}
		if directiveShape.MatchString(lit) && !allowedDirective(lit) {
			return fmt.Errorf("%s: a comment is written like a tool directive (%q), which a linter may read as a suppression", position, lit)
		}
		if reviveDirective.MatchString(lit) {
			return fmt.Errorf("%s: a comment is a revive directive (%q), which revive reads with white space after the marker", position, lit)
		}
	}
}

// allowedDirective reports whether the comment text is one of allowedDirectives.
func allowedDirective(lit string) bool {
	for _, directive := range allowedDirectives {
		if rest, ok := strings.CutPrefix(lit, directive); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			return true
		}
	}
	return false
}

// checkGeneratedHeader refuses the first line of src that starts with generatedPrefix, after any
// leading spaces and tabs, which is a superset of what revive (a line with no leading white space)
// and go/ast.IsGenerated (a comment) read, or that is cgoLine.
func checkGeneratedHeader(path string, src []byte) error {
	for n, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, generatedPrefix) {
			return fmt.Errorf("%s:%d: a line starts with %q, the header that makes linters skip a generated file, even inside a string", path, n+1, generatedPrefix)
		}
		if strings.TrimRight(trimmed, "\r") == cgoLine {
			return fmt.Errorf("%s:%d: a line is %q, the mark that makes linters skip cgo output, even inside a string", path, n+1, cgoLine)
		}
	}
	return nil
}
