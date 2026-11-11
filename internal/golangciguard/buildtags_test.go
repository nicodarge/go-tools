package golangciguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	tagged   = "  tests: true\n  build-tags: [integration]\n"
	untagged = "  tests: true\n"
)

func writeNested(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, dir, name, text)
}

func TestBuildConstraintsAreJudged(t *testing.T) {
	type files map[string]string
	for label, tc := range map[string]struct {
		files  files
		config string
		want   string // empty: accepted
	}{
		"a plain file":                            {files{"a.go": "package p\n"}, validConfig, ""},
		"no Go file":                              {files{"README.md": "x\n"}, validConfig, ""},
		"a linux-only file":                       {files{"a.go": "//go:build linux\n\npackage p\n"}, validConfig, ""},
		"a linux and amd64 file":                  {files{"a.go": "//go:build linux && amd64\n\npackage p\n"}, validConfig, ""},
		"a linux and arm64 file":                  {files{"a.go": "//go:build linux && arm64\n\npackage p\n"}, validConfig, ""},
		"a unix file":                             {files{"a.go": "//go:build unix\n\npackage p\n"}, validConfig, ""},
		"a gc file":                               {files{"a.go": "//go:build gc\n\npackage p\n"}, validConfig, ""},
		"a release tag":                           {files{"a.go": "//go:build go1.18\n\npackage p\n"}, validConfig, ""},
		"a not-windows file":                      {files{"a.go": "//go:build !windows\n\npackage p\n"}, validConfig, ""},
		"a _linux file name":                      {files{"a_linux.go": "package p\n"}, validConfig, ""},
		"a _linux_amd64 file name":                {files{"a_linux_amd64.go": "package p\n"}, validConfig, ""},
		"a _linux_arm64_test file name":           {files{"a_linux_arm64_test.go": "package p\n"}, validConfig, ""},
		"an integration file with the tag set":    {files{"a.go": "//go:build integration\n\npackage p\n"}, edit(t, untagged, tagged), ""},
		"an integration test with the tag set":    {files{"a_test.go": "//go:build integration\n\npackage p\n"}, edit(t, untagged, tagged), ""},
		"a plain file with the tag set":           {files{"a.go": "package p\n"}, edit(t, untagged, tagged), ""},
		"a file in a subdirectory":                {files{"sub/a.go": "package p\n"}, validConfig, ""},
		"an ignored file in testdata":             {files{"testdata/a.go": "//go:build ignore\n\npackage p\n"}, validConfig, ""},
		"an ignored file in a nested testdata":    {files{"sub/testdata/a.go": "//go:build ignore\n\npackage p\n"}, validConfig, ""},
		"an ignored file in vendor":               {files{"vendor/a.go": "//go:build ignore\n\npackage p\n"}, validConfig, ""},
		"an ignored file in a dot directory":      {files{".hidden/a.go": "//go:build ignore\n\npackage p\n"}, validConfig, ""},
		"an ignored file in an underscore dir":    {files{"_old/a.go": "//go:build ignore\n\npackage p\n"}, validConfig, ""},
		"an ignored file in a nested module":      {files{"tools/go.mod": "module x\n", "tools/a.go": "//go:build ignore\n\npackage p\n"}, validConfig, ""},
		"an underscore file":                      {files{"_a.go": "//go:build ignore\n\npackage p\n"}, validConfig, ""},
		"a dot file":                              {files{".a.go": "//go:build ignore\n\npackage p\n"}, validConfig, ""},
		"a go:build ignore file":                  {files{"a.go": "//go:build ignore\n\npackage p\n"}, validConfig, "a.go: its build constraint or file name excludes it"},
		"an ignore file in a subdirectory":        {files{"sub/gen.go": "//go:build ignore\n\npackage main\n"}, validConfig, "sub/gen.go: its build constraint or file name excludes it"},
		"a windows file":                          {files{"a.go": "//go:build windows\n\npackage p\n"}, validConfig, "a.go: its build constraint"},
		"a darwin file":                           {files{"a.go": "//go:build darwin\n\npackage p\n"}, validConfig, "a.go: its build constraint"},
		"a constraint false on both arches":       {files{"a.go": "//go:build linux && 386\n\npackage p\n"}, validConfig, "a.go: its build constraint"},
		"a cgo file":                              {files{"a.go": "//go:build cgo\n\npackage p\n"}, validConfig, "a.go: its build constraint"},
		"a future release tag":                    {files{"a.go": "//go:build go1.999\n\npackage p\n"}, validConfig, "a.go: its build constraint"},
		"a tag that is never set":                 {files{"a.go": "//go:build integration\n\npackage p\n"}, validConfig, "a.go: its build constraint"},
		"an equiv file without the tag":           {files{"a.go": "//go:build equiv\n\npackage p\n"}, validConfig, "a.go: its build constraint"},
		"an integration file with another tag":    {files{"a.go": "//go:build integration\n\npackage p\n"}, edit(t, untagged, "  tests: true\n  build-tags: [e2e]\n"), "a.go: its build constraint"},
		"a _windows file name":                    {files{"a_windows.go": "package p\n"}, validConfig, "a_windows.go: its build constraint or file name"},
		"a _darwin_arm64 file name":               {files{"a_darwin_arm64.go": "package p\n"}, validConfig, "a_darwin_arm64.go: its build constraint or file name"},
		"a _linux_386 file name":                  {files{"a_linux_386.go": "package p\n"}, validConfig, "a_linux_386.go: its build constraint or file name"},
		"a _windows_test file name":               {files{"a_windows_test.go": "package p\n"}, validConfig, "a_windows_test.go: its build constraint or file name"},
		"a _windows file name with the tag set":   {files{"a_windows.go": "package p\n"}, edit(t, untagged, tagged), "a_windows.go: its build constraint or file name"},
		"a negated tag":                           {files{"a.go": "//go:build !integration\n\npackage p\n"}, edit(t, untagged, tagged), "built without the run.build-tags but excluded with them"},
		"a negated tag in a conjunction":          {files{"a.go": "//go:build linux && !integration\n\npackage p\n"}, edit(t, untagged, tagged), "built without the run.build-tags but excluded with them"},
		"a negated tag among several":             {files{"a.go": "//go:build !e2e\n\npackage p\n"}, edit(t, untagged, "  tests: true\n  build-tags: [integration, e2e]\n"), "built without the run.build-tags but excluded with them"},
		"a negated tag in a test file":            {files{"sub/a_test.go": "//go:build !integration\n\npackage p\n"}, edit(t, untagged, tagged), "sub/a_test.go: built without the run.build-tags"},
		"a negated tag on one architecture":       {files{"a.go": "//go:build (amd64 && !integration) || arm64\n\npackage p\n"}, edit(t, untagged, tagged), "built without the run.build-tags but excluded with them on linux/amd64"},
		"a negated tag with the tag unset":        {files{"a.go": "//go:build !integration\n\npackage p\n"}, validConfig, ""},
		"a syntax error in the constraint":        {files{"a.go": "//go:build linux &&\n\npackage p\n"}, validConfig, "a.go: cannot read its build constraint"},
		"a refused file after an accepted one":    {files{"a.go": "package p\n", "b.go": "//go:build ignore\n\npackage p\n"}, validConfig, "b.go: its build constraint"},
		"a plus-build line":                       {files{"a.go": "// +build ignore\n\npackage p\n"}, validConfig, "a.go: its build constraint"},
		"an ignore line after the package clause": {files{"a.go": "package p\n\n//go:build ignore\n"}, validConfig, ""},
	} {
		dir := t.TempDir()
		write(t, dir, ".golangci.yaml", tc.config)
		for name, text := range tc.files {
			writeNested(t, dir, name, text)
		}
		err := CheckPath(dir, DefaultRequired)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: refused: %v", label, err)
		case tc.want != "" && err == nil:
			t.Errorf("%s: accepted, want %q", label, tc.want)
		case tc.want != "" && !strings.Contains(err.Error(), tc.want):
			t.Errorf("%s: refused with %q, want %q", label, err, tc.want)
		}
	}
}

func TestBuildConstraintsOfAFileOtherThanARegularOne(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".golangci.yaml", validConfig)
	target := write(t, dir, "real.txt", "package p\n")
	if err := os.Symlink(target, filepath.Join(dir, "a.go")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := CheckPath(dir, DefaultRequired); err == nil || !strings.Contains(err.Error(), "a.go: not a regular file") {
		t.Errorf("a symbolic link named like a Go file: %v", err)
	}
}
