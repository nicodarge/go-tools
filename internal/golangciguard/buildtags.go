package golangciguard

import (
	"fmt"
	"go/build"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var lintedArches = []string{"amd64", "arm64"}

// checkBuildConstraints walks the Go files under root the way the go tool does (it skips testdata,
// vendor, directories starting with "_" or ".", nested modules and files starting with "_" or ".")
// and refuses a file golangci-lint would not lint on linux: one excluded by its build constraint or
// its GOOS/GOARCH file name suffix on linux/amd64 and linux/arm64 with the configured build tags
// (cgo off), or included without them and excluded with them.
func checkBuildConstraints(root *os.Root, tags []string) error {
	fsys := root.FS()
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p == "." {
				return nil
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" {
				return fs.SkipDir
			}
			if _, statErr := root.Lstat(filepath.Join(p, "go.mod")); statErr == nil {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", p)
		}
		return checkGoFile(root, p, tags)
	})
}

func checkGoFile(root *os.Root, p string, tags []string) error {
	dir, name := path.Split(p)
	if dir == "" {
		dir = "."
	}
	linted := false
	for _, arch := range lintedArches {
		without, err := matches(root, dir, name, arch, nil)
		if err != nil {
			return fmt.Errorf("%s: cannot read its build constraint: %w", p, err)
		}
		with, err := matches(root, dir, name, arch, tags)
		if err != nil {
			return fmt.Errorf("%s: cannot read its build constraint: %w", p, err)
		}
		if without && !with {
			return fmt.Errorf("%s: built without the run.build-tags but excluded with them on linux/%s, so a negated tag takes it out of the lint", p, arch)
		}
		linted = linted || with
	}
	if !linted {
		return fmt.Errorf("%s: its build constraint or file name excludes it on linux/amd64 and linux/arm64, so golangci-lint never lints it", p)
	}
	return nil
}

func matches(root *os.Root, dir, name, arch string, tags []string) (bool, error) {
	ctxt := build.Context{
		GOOS:        "linux",
		GOARCH:      arch,
		Compiler:    "gc",
		ReleaseTags: build.Default.ReleaseTags,
		BuildTags:   tags,
		OpenFile:    func(p string) (io.ReadCloser, error) { return root.Open(p) },
	}
	return ctxt.MatchFile(filepath.FromSlash(dir), name)
}
