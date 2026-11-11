package golangciguard

import (
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// suppressionWords are the strings with which a comment suppresses a finding of golangci-lint or of
// a linter it builds in; each was confirmed in the source of the pinned golangci-lint and of the
// linter modules it builds. The README names the directive behind each.
var suppressionWords = []string{
	"nolint", "nosec", "gosec:", "revive:", "exhaustive:", "gocyclo:", "gocognit:", "iface:",
	"exhaustruct:", "godoclint:", "nozero",
}

// lineDirectives are the prefixes of a line directive: golangci-lint's cgo processor maps a position
// that a //line directive puts in a file named like cgo output (_cgo_gotypes.go) away, and drops
// the finding.
var lineDirectives = []string{"//line", "/*line"}

// ScanGo returns an error when the Go source at path cannot be read or tokenized, or has a comment
// (line or block) that holds one of suppressionWords in any case and any spacing, or that starts
// with one of lineDirectives in any case and at any column. String and rune literals are not
// comments and are not looked at.
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
		lower := strings.ToLower(lit)
		for _, prefix := range lineDirectives {
			if strings.HasPrefix(lower, prefix) {
				return fmt.Errorf("%s: a comment starts with %q, a line directive that moves findings to another file", file.PositionFor(pos, false), prefix)
			}
		}
		for _, word := range suppressionWords {
			if strings.Contains(lower, word) {
				return fmt.Errorf("%s: a comment holds %q", file.PositionFor(pos, false), word)
			}
		}
	}
}
