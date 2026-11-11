package golangciguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nestedModule = "module example.com/nested\n"

func TestModuleFilesAreJudged(t *testing.T) {
	nested := files{"nested/go.mod": nestedModule, "nested/.golangci.yaml": validConfig, "nested/a.go": "package n\n"}
	with := func(extra files) files {
		out := files{"main.go": "package main\n"}
		for k, v := range nested {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	for label, tc := range map[string]struct {
		files files
		want  string // empty: accepted
	}{
		"a plain go.mod":                    {files{"go.mod": "module m\n\ngo 1.27\n", "a.go": "package p\n"}, ""},
		"a require and a module replace":    {files{"go.mod": "module m\n\nrequire x.y/z v1.0.0\n\nreplace x.y/z => x.y/w v1.1.0\n", "a.go": "package p\n"}, ""},
		"an exclude and a tool":             {files{"go.mod": "module m\n\nexclude x.y/z v1.0.0\n\ntool x.y/z/cmd/t\n", "a.go": "package p\n"}, ""},
		"a retract":                         {files{"go.mod": "module m\n\nretract v1.0.0\n", "a.go": "package p\n"}, ""},
		"a git directory is not entered":    {files{".git/go.work": "go 1.27\n", ".git/x/go.mod": "module m\n\nignore ./a\n"}, ""},
		"no go.mod at all":                  {files{"a.go": "package p\n"}, ""},
		"a go.mod nobody parses is refused": {files{"go.mod": "module m\n\nfrobnicate x\n"}, "go.mod: cannot read the module file"},
		"an unterminated block":             {files{"go.mod": "module m\n\nrequire (\n"}, "go.mod: cannot read the module file"},

		"an ignore directive":                 {files{"go.mod": "module m\n\nignore ./a\n", "a/x.go": "package a\n"}, "go.mod: an ignore directive (./a) hides a directory from ./..."},
		"an ignore directive, block form":     {files{"go.mod": "module m\n\nignore (\n\t./a\n\t./b\n)\n", "a/x.go": "package a\n"}, "an ignore directive (./a)"},
		"an ignore of a nested directory":     {files{"go.mod": "module m\n\nignore ./a/b\n", "a/b/x.go": "package b\n"}, "an ignore directive (./a/b)"},
		"an ignore of a bare name":            {files{"go.mod": "module m\n\nignore gen\n", "gen/x.go": "package gen\n"}, "an ignore directive (gen)"},
		"an ignore in a nested module":        {with(files{"nested/go.mod": nestedModule + "\nignore ./sub\n"}), "nested/go.mod: an ignore directive (./sub)"},
		"an ignore in a Go-less nested":       {files{"nested/go.mod": nestedModule + "\nignore ./sub\n"}, "nested/go.mod: an ignore directive (./sub)"},
		"an ignore with the import":           {files{"go.mod": "module m\n\nignore ./a\n", "main.go": "package main\n\nimport _ \"m/a\"\n", "a/x.go": "package a\n"}, "go.mod: an ignore directive"},
		"a go.mod symbolic link":              {files{"real.mod": "module m\n"}, ""},
		"a go.mod that is a directory":        {files{"sub/go.mod/x": "x\n"}, "sub/go.mod: not a regular file"},
		"a go.mod in testdata":                {files{"testdata/x/go.mod": "module x\n"}, "testdata/x/go.mod: a module in a directory the go tool skips (testdata)"},
		"a go.mod in a deep testdata":         {files{"sub/testdata/go.mod": "module x\n"}, "sub/testdata/go.mod: a module in a directory the go tool skips (testdata)"},
		"a go.mod in a nested testdata":       {with(files{"nested/testdata/x/go.mod": "module x\n"}), "nested/testdata/x/go.mod: a module in a directory the go tool skips (testdata)"},
		"a go.mod in an underscore directory": {files{"_x/go.mod": "module x\n"}, "_x/go.mod: a module in a directory the go tool skips (_x)"},
		"a go.mod in a dot directory":         {files{".x/y/go.mod": "module x\n"}, ".x/y/go.mod: a module in a directory the go tool skips (.x)"},
		"a go.mod in a vendor directory":      {files{"vendor/x/go.mod": "module x\n"}, "vendor/x/go.mod: a module in a directory the go tool skips (vendor)"},
		"a go.mod in a vendor, no Go file":    {files{"sub/vendor/go.mod": "module x\n", "sub/vendor/modules.txt": "x\n"}, "sub/vendor/go.mod: a module in a directory the go tool skips (vendor)"},

		"a go.work":                        {files{"go.work": "go 1.27\n\nuse .\n"}, "go.work: a workspace file"},
		"a go.work.sum":                    {files{"go.work.sum": "x\n"}, "go.work.sum: a workspace file"},
		"a go.work in a subdirectory":      {files{"sub/go.work": "go 1.27\n"}, "sub/go.work: a workspace file"},
		"a go.work in testdata":            {files{"testdata/go.work": "go 1.27\n"}, "testdata/go.work: a workspace file"},
		"a go.work in an underscore dir":   {files{"_x/go.work": "go 1.27\n"}, "_x/go.work: a workspace file"},
		"a go.work in a vendor directory":  {files{"vendor/go.work": "go 1.27\n"}, "vendor/go.work: a workspace file"},
		"a go.work.sum in testdata":        {files{"testdata/x/go.work.sum": "x\n"}, "testdata/x/go.work.sum: a workspace file"},
		"a go.work in a Go-less module":    {files{"nested/go.mod": nestedModule, "nested/go.work": "go 1.27\n"}, "nested/go.work: a workspace file"},
		"a go.work in a configured module": {with(files{"nested/go.work": "go 1.27\n"}), "nested/go.work: a workspace file"},
		"a go.work directory":              {files{"go.work/x": "x\n"}, "go.work: a workspace file"},
		"a go.work referring to testdata":  {files{"go.mod": "module m\n", "go.work": "go 1.27\n\nuse (\n\t.\n\t./testdata/x\n)\n", "testdata/x/go.mod": "module x\n", "testdata/x/x.go": "package x\n"}, "go.work: a workspace file"},

		"a replace by a module path":                  {files{"go.mod": "module m\n\nreplace x.y/z => x.y/w v1.0.0\n"}, ""},
		"a replace by a module path that is dotted":   {files{"go.mod": "module m\n\nreplace x.y/z => .y.example/w v1.0.0\n"}, ""},
		"a replace by a nested module":                {with(files{"go.mod": "module m\n\nreplace example.com/nested => ./nested\n"}), ""},
		"a replace by a nested module, block form":    {with(files{"go.mod": "module m\n\nreplace (\n\texample.com/nested => ./nested\n)\n"}), ""},
		"a replace by a nested module, unclean":       {with(files{"go.mod": "module m\n\nreplace example.com/nested => ./sub/../nested/\n"}), ""},
		"a replace by a Go-less nested module":        {files{"go.mod": "module m\n\nreplace example.com/nested => ./nested\n", "nested/go.mod": nestedModule}, ""},
		"a replace by the module itself":              {files{"go.mod": "module m\n\nreplace example.com/nested => .\n"}, ""},
		"a replace of the root from a nested one":     {with(files{"go.mod": "module m\n", "nested/go.mod": nestedModule + "\nreplace m => ../\n"}), ""},
		"a replace of a sibling from a nested one":    {with(files{"sibling/go.mod": "module example.com/sibling\n", "sibling/.golangci.yaml": validConfig, "nested/go.mod": nestedModule + "\nreplace example.com/sibling => ../sibling\n"}), ""},
		"a replace by a nested module with no config": {files{"go.mod": "module m\n\nreplace example.com/nested => ./nested\n", "nested/go.mod": nestedModule, "nested/a.go": "package n\n"}, "nested holds no .golangci."},
		"a replace by testdata":                       {files{"go.mod": "module m\n\nreplace example.com/x => ./testdata/x\n", "testdata/x/x.go": "package x\n"}, "the replace directive for example.com/x: \"./testdata/x\" is in a directory the go tool skips (testdata)"},
		"a replace by testdata, module in it":         {files{"go.mod": "module m\n\nreplace example.com/x => ./testdata/x\n", "testdata/x/go.mod": "module example.com/x\n", "testdata/x/x.go": "package x\n"}, "is in a directory the go tool skips (testdata)"},
		"a replace by an underscore directory":        {files{"go.mod": "module m\n\nreplace example.com/x => ./_x\n", "_x/go.mod": "module example.com/x\n"}, "a directory the go tool skips (_x)"},
		"a replace by a dot directory":                {files{"go.mod": "module m\n\nreplace example.com/x => ./.x\n"}, "\"./.x\" is in a directory the go tool skips (.x)"},
		"a replace by a vendor directory":             {files{"go.mod": "module m\n\nreplace example.com/x => ./vendor/x\n"}, "\"./vendor/x\" is in a directory the go tool skips (vendor)"},
		"a replace by a nested testdata":              {with(files{"nested/go.mod": nestedModule + "\nreplace example.com/x => ./testdata/x\n"}), "nested/go.mod: the replace directive for example.com/x: \"./testdata/x\" is in a directory the go tool skips (testdata)"},
		"a replace outside the directory":             {files{"go.mod": "module m\n\nreplace example.com/x => ../x\n"}, "\"../x\" leaves the directory checked"},
		"a replace of the parent":                     {files{"go.mod": "module m\n\nreplace example.com/x => ..\n"}, "\"..\" leaves the directory checked"},
		"a replace leaving through a subdirectory":    {files{"go.mod": "module m\n\nreplace example.com/x => ./a/../../x\n"}, "leaves the directory checked"},
		"a replace from a nested module outside":      {with(files{"nested/go.mod": nestedModule + "\nreplace example.com/x => ../../x\n"}), "nested/go.mod: the replace directive for example.com/x: \"../../x\" leaves the directory checked"},
		"an absolute replace":                         {files{"go.mod": "module m\n\nreplace example.com/x => /opt/x\n"}, "\"/opt/x\" is an absolute path"},
		"a replace by a missing directory":            {files{"go.mod": "module m\n\nreplace example.com/x => ./missing\n"}, "cannot read \"./missing\""},
		"a replace by a directory with no go.mod":     {files{"go.mod": "module m\n\nreplace example.com/x => ./plain\n", "plain/a.go": "package p\n"}, "\"./plain\" does not hold a go.mod that is a regular file"},
		"a replace by a go.mod directory":             {files{"go.mod": "module m\n\nreplace example.com/x => ./plain\n", "plain/go.mod/x": "x\n"}, "plain/go.mod: not a regular file"},
		"a replace by a file":                         {files{"go.mod": "module m\n\nreplace example.com/x => ./file\n", "file": "x\n"}, "\"./file\" goes through file, which is not a directory"},
		"a replace through a file":                    {files{"go.mod": "module m\n\nreplace example.com/x => ./file/x\n", "file": "x\n"}, "\"./file/x\" goes through file, which is not a directory"},
	} {
		t.Run(label, func(t *testing.T) {
			if label == "a go.mod symbolic link" {
				dir := tree(t, validConfig, tc.files)
				if err := os.Symlink("real.mod", filepath.Join(dir, "go.mod")); err != nil {
					t.Skipf("no symbolic links here: %v", err)
				}
				check(t, dir, "go.mod: not a regular file")
				return
			}
			check(t, tree(t, validConfig, tc.files), tc.want)
		})
	}
}

func check(t *testing.T, dir, want string) {
	t.Helper()
	err := CheckPath(dir, DefaultRequired)
	switch {
	case want == "" && err != nil:
		t.Errorf("refused: %v", err)
	case want != "" && err == nil:
		t.Errorf("accepted, want %q", want)
	case want != "" && !strings.Contains(err.Error(), want):
		t.Errorf("refused with %q, want %q", err, want)
	}
}

// The walk of the Go files refuses a symbolic link to a directory on its own; the replace check does
// not rely on that, so it is driven directly.
func TestReplaceBehindASymbolicLinkIsRefused(t *testing.T) {
	for label, tc := range map[string]struct {
		link, target, replace, want string
	}{
		"a link to a nested module":        {"link", "nested", "./link", "\"./link\" goes through link, which is not a directory"},
		"a link to testdata":               {"link", "testdata/x", "./link", "\"./link\" goes through link, which is not a directory"},
		"a link in the middle of the path": {"sub/link", "../nested", "./sub/link", "goes through sub/link, which is not a directory"},
	} {
		t.Run(label, func(t *testing.T) {
			dir := tree(t, validConfig, files{
				"go.mod":        "module m\n\nreplace example.com/nested => " + tc.replace + "\n",
				"nested/go.mod": nestedModule, "testdata/x/y": "x\n", "sub/z": "x\n",
			})
			if err := os.Symlink(tc.target, filepath.Join(dir, tc.link)); err != nil {
				t.Skipf("no symbolic links here: %v", err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			if err = checkModuleFiles(root, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refused with %v, want %q", err, tc.want)
			}
		})
	}
}

func TestANestedModuleWithOnlyASymbolicLinkToADirectory(t *testing.T) {
	for label, tc := range map[string]struct {
		files  files
		parent string
		target string
		want   string
	}{
		"needs a configuration": {
			files{"nested/go.mod": nestedModule, "nested/sub/y": "x\n"}, "nested", "sub",
			"nested holds no .golangci.",
		},
		"is then refused for the link": {
			files{"nested/go.mod": nestedModule, "nested/.golangci.yaml": validConfig, "nested/sub/y": "x\n"}, "nested", "sub",
			"nested/link: a symbolic link to a directory",
		},
		"to a directory outside the module": {
			files{"nested/go.mod": nestedModule, "nested/.golangci.yaml": validConfig, "outside/o.go": "package o\n"}, "nested", "../outside",
			"nested/link: cannot resolve the symbolic link",
		},
		"in a directory the go tool skips": {
			files{"_x/y": "x\n", "z/a.go": "package z\n"}, "_x", "../z",
			"_x/link: a Go file in a directory the go tool skips (_x)",
		},
	} {
		t.Run(label, func(t *testing.T) {
			dir := tree(t, validConfig, tc.files)
			if err := os.Symlink(tc.target, filepath.Join(dir, tc.parent, "link")); err != nil {
				t.Skipf("no symbolic links here: %v", err)
			}
			check(t, dir, tc.want)
		})
	}

	t.Run("a dangling link is nothing to lint", func(t *testing.T) {
		dir := tree(t, validConfig, files{"nested/go.mod": nestedModule})
		if err := os.Symlink("missing", filepath.Join(dir, "nested", "link")); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		check(t, dir, "")
	})
	t.Run("a link to a file is nothing to lint", func(t *testing.T) {
		dir := tree(t, validConfig, files{"nested/go.mod": nestedModule, "nested/real.txt": "x\n"})
		if err := os.Symlink("real.txt", filepath.Join(dir, "nested", "link")); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		check(t, dir, "")
	})
}
