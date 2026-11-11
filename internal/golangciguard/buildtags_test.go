package golangciguard

import (
	"go/build"
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

type files map[string]string

// tree writes a configuration and files into a new directory and returns it.
func tree(t *testing.T, config string, fs files) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, ".golangci.yaml", config)
	for name, text := range fs {
		writeNested(t, dir, name, text)
	}
	return dir
}

func goFile(header string) string { return header + "\npackage p\n" }

func TestBuildConstraintsAreAllowlisted(t *testing.T) {
	integration := edit(t, untagged, tagged)
	e2eConfig := edit(t, untagged, "  tests: true\n  build-tags: [e2e]\n")
	twoTags := edit(t, untagged, "  tests: true\n  build-tags: [integration, e2e]\n")
	for label, tc := range map[string]struct {
		files  files
		config string
		want   string // empty: accepted
	}{
		"a plain file":                       {files{"a.go": "package p\n"}, validConfig, ""},
		"no Go file":                         {files{"README.md": "x\n"}, validConfig, ""},
		"linux":                              {files{"a.go": goFile("//go:build linux\n")}, validConfig, ""},
		"unix":                               {files{"a.go": goFile("//go:build unix\n")}, validConfig, ""},
		"gc":                                 {files{"a.go": goFile("//go:build gc\n")}, validConfig, ""},
		"linux and gc":                       {files{"a.go": goFile("//go:build linux && gc\n")}, validConfig, ""},
		"a disjunction of allowed tags":      {files{"a.go": goFile("//go:build (linux || unix) && gc\n")}, validConfig, ""},
		"a negated negation of an allowed":   {files{"a.go": goFile("//go:build !(!linux)\n")}, validConfig, ""},
		"linux and a configured tag":         {files{"a.go": goFile("//go:build linux && integration\n")}, integration, ""},
		"a configured tag":                   {files{"a_test.go": goFile("//go:build integration\n")}, integration, ""},
		"a plain file with a tag configured": {files{"a.go": "package p\n"}, integration, ""},
		"two configured tags":                {files{"a.go": goFile("//go:build integration && e2e\n")}, twoTags, ""},
		"a _linux file name":                 {files{"a_linux.go": "package p\n"}, validConfig, ""},
		"a _linux_test file name":            {files{"a_linux_test.go": "package p\n"}, validConfig, ""},
		"a _unix file name":                  {files{"a_unix.go": "package p\n"}, validConfig, ""},
		"a file named like an os":            {files{"windows.go": "package p\n"}, validConfig, ""},
		"a file in a subdirectory":           {files{"sub/a.go": "package p\n"}, validConfig, ""},
		"an underscore file":                 {files{"_a.go": goFile("//go:build ignore\n")}, validConfig, ""},
		"a dot file":                         {files{".a.go": goFile("//go:build ignore\n")}, validConfig, ""},
		"an ignore line after the package":   {files{"a.go": "package p\n\n//go:build ignore\n"}, validConfig, ""},
		"an ignored file in testdata":        {files{"testdata/a.go": goFile("//go:build ignore\n")}, validConfig, ""},
		"an ignored file in a deep testdata": {files{"sub/testdata/x/a.go": goFile("//go:build windows\n")}, validConfig, ""},
		"a testdata fixture nobody imports":  {files{"testdata/a.go": "package p\n", "b.go": "package p\n\nimport \"fmt\"\n"}, validConfig, ""},
		"an import that only looks like it":  {files{"a.go": "package p\n\nimport (\n\t_ \"m/mytestdata\"\n\t_ \"m/testdatax\"\n)\n"}, validConfig, ""},
		"a nested module with a config":      {files{"tools/go.mod": "module x\n", "tools/.golangci.yaml": validConfig, "tools/a.go": "package p\n"}, validConfig, ""},
		"a nested module with Go-less files": {files{"tools/go.mod": "module x\n", "tools/go.sum": "\n"}, validConfig, ""},
		"a nested module with its own tag":   {files{"tools/go.mod": "module x\n", "tools/.golangci.yaml": integration, "tools/a.go": goFile("//go:build integration\n")}, validConfig, ""},

		"ignore":                   {files{"a.go": goFile("//go:build ignore\n")}, validConfig, "a.go: its build constraint names \"ignore\""},
		"ignore in a subdirectory": {files{"sub/gen.go": goFile("//go:build ignore\n")}, validConfig, "sub/gen.go: its build constraint names \"ignore\""},
		"tools":                    {files{"a.go": goFile("//go:build tools\n")}, validConfig, "a.go: its build constraint names \"tools\""},
		"an unknown tag":           {files{"a.go": goFile("//go:build foo\n")}, validConfig, "names \"foo\""},
		"a configured tag that is not the one set": {files{"a.go": goFile("//go:build integration\n")}, e2eConfig, "names \"integration\""},
		"an unset tag":                         {files{"a.go": goFile("//go:build integration\n")}, validConfig, "names \"integration\""},
		"windows":                              {files{"a.go": goFile("//go:build windows\n")}, validConfig, "names \"windows\""},
		"darwin":                               {files{"a.go": goFile("//go:build darwin\n")}, validConfig, "names \"darwin\""},
		"not windows":                          {files{"a.go": goFile("//go:build !windows\n")}, validConfig, "names \"windows\""},
		"cgo":                                  {files{"a.go": goFile("//go:build cgo\n")}, validConfig, "names \"cgo\""},
		"not cgo":                              {files{"a.go": goFile("//go:build !cgo\n")}, validConfig, "names \"cgo\""},
		"amd64":                                {files{"a.go": goFile("//go:build amd64\n")}, validConfig, "names \"amd64\""},
		"linux and amd64":                      {files{"a.go": goFile("//go:build linux && amd64\n")}, validConfig, "names \"amd64\""},
		"linux and arm64":                      {files{"a.go": goFile("//go:build linux && arm64\n")}, validConfig, "names \"arm64\""},
		"arm64":                                {files{"a.go": goFile("//go:build arm64\n")}, validConfig, "names \"arm64\""},
		"not amd64":                            {files{"a.go": goFile("//go:build !amd64\n")}, validConfig, "names \"amd64\""},
		"386":                                  {files{"a.go": goFile("//go:build linux && 386\n")}, validConfig, "names \"386\""},
		"a microarchitecture level":            {files{"a.go": goFile("//go:build !amd64.v1\n")}, validConfig, "names \"amd64.v1\""},
		"a goexperiment":                       {files{"a.go": goFile("//go:build goexperiment.swissmap\n")}, validConfig, "names \"goexperiment.swissmap\""},
		"not a goexperiment":                   {files{"a.go": goFile("//go:build !goexperiment.swissmap\n")}, validConfig, "names \"goexperiment.swissmap\""},
		"a release tag":                        {files{"a.go": goFile("//go:build go1.18\n")}, validConfig, "names \"go1.18\""},
		"a release tag too new":                {files{"a.go": goFile("//go:build go1.27\n")}, validConfig, "names \"go1.27\""},
		"a negated release tag":                {files{"a.go": goFile("//go:build !go1.99\n")}, validConfig, "names \"go1.99\""},
		"a bad right operand":                  {files{"a.go": goFile("//go:build linux || windows\n")}, validConfig, "names \"windows\""},
		"a bad left operand":                   {files{"a.go": goFile("//go:build windows || linux\n")}, validConfig, "names \"windows\""},
		"not linux":                            {files{"a.go": goFile("//go:build !linux\n")}, validConfig, "negates \"linux\""},
		"not unix":                             {files{"a.go": goFile("//go:build !unix\n")}, validConfig, "negates \"unix\""},
		"not gc":                               {files{"a.go": goFile("//go:build !gc\n")}, validConfig, "negates \"gc\""},
		"not linux in a disjunction":           {files{"a.go": goFile("//go:build !linux || gc\n")}, validConfig, "negates \"linux\""},
		"not an allowed one, by De Morgan":     {files{"a.go": goFile("//go:build !(linux && gc)\n")}, validConfig, "negates"},
		"a negated configured tag":             {files{"a.go": goFile("//go:build !integration\n")}, integration, "negates the run.build-tags entry \"integration\""},
		"a negated tag in a conjunction":       {files{"a.go": goFile("//go:build linux && !integration\n")}, integration, "negates the run.build-tags entry"},
		"a negated tag among several":          {files{"a.go": goFile("//go:build !e2e\n")}, twoTags, "negates the run.build-tags entry \"e2e\""},
		"a negated tag in a test file":         {files{"sub/a_test.go": goFile("//go:build !integration\n")}, integration, "sub/a_test.go: its build constraint negates the run.build-tags"},
		"a negated tag on one side":            {files{"a.go": goFile("//go:build (linux && !integration) || gc\n")}, integration, "negates the run.build-tags entry"},
		"a double negation":                    {files{"a.go": goFile("//go:build !!linux\n")}, validConfig, "a.go: cannot read its build constraint"},
		"a syntax error":                       {files{"a.go": goFile("//go:build linux &&\n")}, validConfig, "a.go: cannot read its build constraint"},
		"a plus-build line":                    {files{"a.go": goFile("// +build ignore\n")}, validConfig, "names \"ignore\""},
		"a plus-build line next to go:build":   {files{"a.go": goFile("//go:build linux\n// +build windows\n")}, validConfig, "names \"windows\""},
		"two go:build lines":                   {files{"a.go": goFile("//go:build linux\n//go:build linux\n")}, validConfig, "more than one //go:build line"},
		"a refused file after an accepted one": {files{"a.go": "package p\n", "b.go": goFile("//go:build ignore\n")}, validConfig, "b.go: its build constraint"},
		"a syntax error in the header":         {files{"a.go": "package\n"}, validConfig, "a.go: cannot read its build constraint"},
		"a _windows file name":                 {files{"a_windows.go": "package p\n"}, validConfig, "a_windows.go: its file name carries a GOOS or GOARCH suffix"},
		"a _darwin file name":                  {files{"a_darwin.go": "package p\n"}, validConfig, "a_darwin.go: its file name"},
		"a _darwin_arm64 file name":            {files{"a_darwin_arm64.go": "package p\n"}, validConfig, "a_darwin_arm64.go: its file name"},
		"a _android file name":                 {files{"a_android.go": "package p\n"}, validConfig, "a_android.go: its file name"},
		"a _arm64 file name":                   {files{"a_arm64.go": "package p\n"}, validConfig, "a_arm64.go: its file name"},
		"an _amd64 file name":                  {files{"sub/a_amd64.go": "package p\n"}, validConfig, "sub/a_amd64.go: its file name"},
		"a _386 file name":                     {files{"a_386.go": "package p\n"}, validConfig, "a_386.go: its file name"},
		"a _linux_amd64 file name":             {files{"a_linux_amd64.go": "package p\n"}, validConfig, "a_linux_amd64.go: its file name"},
		"a _linux_arm64_test file name":        {files{"a_linux_arm64_test.go": "package p\n"}, validConfig, "a_linux_arm64_test.go: its file name"},
		"a _windows_test file name":            {files{"a_windows_test.go": "package p\n"}, validConfig, "a_windows_test.go: its file name"},
		"a _windows name, tag set":             {files{"a_windows.go": "package p\n"}, integration, "a_windows.go: its file name"},
		"an import of C":                       {files{"a.go": "package p\n\nimport \"C\"\n"}, validConfig, "a.go: it imports \"C\""},
		"an import of C in a group":            {files{"a.go": "package p\n\nimport (\n\t\"fmt\"\n\t\"C\"\n)\n"}, validConfig, "it imports \"C\""},
		"a preamble before import of C":        {files{"a.go": "package p\n\n// #include <stdio.h>\nimport \"C\"\n"}, validConfig, "it imports \"C\""},
		"a testdata import":                    {files{"a.go": "package p\n\nimport _ \"m/pkg/testdata/fixture\"\n", "pkg/testdata/fixture/f.go": "package fixture\n"}, validConfig, "a.go: it imports \"m/pkg/testdata/fixture\""},
		"a testdata import of the dir":         {files{"sub/a.go": "package p\n\nimport _ \"m/testdata\"\n"}, validConfig, "sub/a.go: it imports \"m/testdata\""},
		"a go.mod directory":                   {files{"sub/go.mod/x": "x\n", "sub/a.go": goFile("//go:build ignore\n")}, validConfig, "sub/go.mod: not a regular file"},
		"a vendor directory":                   {files{"vendor/a.go": "package p\n"}, validConfig, "vendor/a.go: a Go file in a directory the go tool skips (vendor)"},
		"a deep vendor directory":              {files{"sub/vendor/x/a.go": "package p\n"}, validConfig, "sub/vendor/x/a.go: a Go file in a directory the go tool skips (vendor)"},
		"an underscore directory":              {files{"_x/a.go": "package p\n"}, validConfig, "_x/a.go: a Go file in a directory the go tool skips (_x)"},
		"a dot directory":                      {files{".x/a.go": "package p\n"}, validConfig, ".x/a.go: a Go file in a directory the go tool skips (.x)"},
		"an ignored file in a dot dir":         {files{".hidden/a.go": goFile("//go:build ignore\n")}, validConfig, ".hidden/a.go: a Go file in a directory"},
		"a Go file deep in an underscore":      {files{"_x/y/z/a.go": "package p\n"}, validConfig, "_x/y/z/a.go: a Go file in a directory"},
		"a nested module without config":       {files{"tools/go.mod": "module x\n", "tools/a.go": "package p\n"}, validConfig, "tools holds no .golangci."},
		"a nested module, ignored file":        {files{"tools/go.mod": "module x\n", "tools/a.go": goFile("//go:build ignore\n")}, validConfig, "tools holds no .golangci."},
		"a nested module, bad config":          {files{"tools/go.mod": "module x\n", "tools/.golangci.yaml": edit(t, "max-same-issues: 0", "max-same-issues: 1"), "tools/a.go": "package p\n"}, validConfig, "max-same-issues must be exactly 0"},
		"a nested module, bad constraint":      {files{"tools/go.mod": "module x\n", "tools/.golangci.yaml": validConfig, "tools/sub/a.go": goFile("//go:build windows\n")}, validConfig, "tools/sub/a.go: its build constraint names \"windows\""},
		"a nested module, tag not set":         {files{"tools/go.mod": "module x\n", "tools/.golangci.yaml": validConfig, "tools/a.go": goFile("//go:build integration\n")}, integration, "tools/a.go: its build constraint names \"integration\""},
		"a module nested twice":                {files{"a/go.mod": "module a\n", "a/.golangci.yaml": validConfig, "a/b/go.mod": "module b\n", "a/b/c.go": "package p\n"}, validConfig, "a/b holds no .golangci."},
		"a vendor directory in a module":       {files{"tools/go.mod": "module x\n", "tools/.golangci.yaml": validConfig, "tools/vendor/a.go": "package p\n"}, validConfig, "tools/vendor/a.go: a Go file in a directory"},
	} {
		t.Run(label, func(t *testing.T) {
			dir := tree(t, tc.config, tc.files)
			err := CheckPath(dir, DefaultRequired)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.want != "" && err == nil:
				t.Errorf("accepted, want %q", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Errorf("refused with %q, want %q", err, tc.want)
			}
		})
	}
}

// The guard accepts a file only when go/build, which golangci-lint relies on, builds it in every
// environment a lint run may have.
func TestAcceptedFilesAreBuiltInEveryEnvironment(t *testing.T) {
	for label, tc := range map[string]struct {
		name, text string
		tags       []string
	}{
		"a plain file":            {"a.go", "package p\n", nil},
		"linux":                   {"a.go", goFile("//go:build linux\n"), nil},
		"unix":                    {"a.go", goFile("//go:build unix\n"), nil},
		"gc":                      {"a.go", goFile("//go:build gc\n"), nil},
		"a disjunction":           {"a.go", goFile("//go:build (linux || unix) && gc\n"), nil},
		"a configured tag":        {"a.go", goFile("//go:build linux && integration\n"), []string{"integration"}},
		"a plus-build line":       {"a.go", goFile("// +build linux\n"), nil},
		"a _linux file name":      {"a_linux.go", "package p\n", nil},
		"a _linux_test file name": {"a_linux_test.go", "package p\n", nil},
		"a _unix file name":       {"a_unix.go", "package p\n", nil},
	} {
		t.Run(label, func(t *testing.T) {
			config := validConfig
			if tc.tags != nil {
				config = edit(t, untagged, tagged)
			}
			dir := tree(t, config, files{tc.name: tc.text})
			if err := CheckPath(dir, DefaultRequired); err != nil {
				t.Fatalf("refused: %v", err)
			}
			for _, arch := range []string{"amd64", "arm64"} {
				for _, cgo := range []bool{false, true} {
					ctxt := build.Context{
						GOOS: "linux", GOARCH: arch, CgoEnabled: cgo, Compiler: "gc",
						ReleaseTags: build.Default.ReleaseTags, BuildTags: tc.tags,
					}
					if ok, err := ctxt.MatchFile(dir, tc.name); err != nil || !ok {
						t.Errorf("accepted, but go/build does not build it on linux/%s with cgo=%v: %v", arch, cgo, err)
					}
				}
			}
		})
	}
}

// Every file name the guard refuses has a GOOS or GOARCH suffix go/build reads, and the other way
// round: the table is the toolchain's own list of systems and architectures.
func TestFileNameSuffixesFollowTheToolchain(t *testing.T) {
	for _, suffix := range []string{
		"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "nacl",
		"netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos",
		"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64", "mips64le", "mipsle", "ppc64",
		"ppc64le", "riscv64", "s390x", "wasm",
		"linux_amd64", "linux_arm64", "darwin_arm64", "windows_amd64", "js_wasm",
	} {
		for _, name := range []string{"a_" + suffix + ".go", "a_" + suffix + "_test.go"} {
			if ok, err := fileNameIsLinux(name); err != nil || ok {
				t.Errorf("%s: accepted (%v)", name, err)
			}
		}
	}
	for _, name := range []string{"a.go", "a_test.go", "a_linux.go", "a_linux_test.go", "a_unix.go", "a_b_c.go", "linux.go"} {
		if ok, err := fileNameIsLinux(name); err != nil || !ok {
			t.Errorf("%s: refused (%v)", name, err)
		}
	}
}

func TestASymlinkedDirectoryIsRefused(t *testing.T) {
	for label, tc := range map[string]struct {
		link, target string
		want         string
	}{
		"a link to a directory in the tree": {"link", "real", "link: a symbolic link to a directory"},
		"a link in a subdirectory":          {"sub/link", "../real", "sub/link: a symbolic link to a directory"},
		"a link to a directory outside":     {"link", "..", "link: cannot resolve the symbolic link"},
	} {
		t.Run(label, func(t *testing.T) {
			dir := tree(t, validConfig, files{"real/a.go": "package p\n", "sub/b.go": "package p\n"})
			if err := os.Symlink(tc.target, filepath.Join(dir, tc.link)); err != nil {
				t.Skipf("no symbolic links here: %v", err)
			}
			if err := CheckPath(dir, DefaultRequired); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refused with %v, want %q", err, tc.want)
			}
		})
	}
}

func TestASymlinkToAFileIsAccepted(t *testing.T) {
	dir := tree(t, validConfig, files{"real.txt": "x\n", "a.go": "package p\n"})
	if err := os.Symlink("real.txt", filepath.Join(dir, "link")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := CheckPath(dir, DefaultRequired); err != nil {
		t.Errorf("refused: %v", err)
	}
}

func TestBuildConstraintsOfAFileOtherThanARegularOne(t *testing.T) {
	dir := tree(t, validConfig, files{"real.txt": "package p\n"})
	if err := os.Symlink("real.txt", filepath.Join(dir, "a.go")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := CheckPath(dir, DefaultRequired); err == nil || !strings.Contains(err.Error(), "a.go: not a regular file") {
		t.Errorf("a symbolic link named like a Go file: %v", err)
	}
}
