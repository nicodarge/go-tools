package checksumcheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicodarge/go-tools/internal/core"
)

func toolsHeader(body string) string {
	return "from dataclasses import dataclass\n\nTOOLS = (\n" + body + ")\n"
}

func TestParseToolsAccepts(t *testing.T) {
	cases := []struct {
		label, text string
		prefixes    string
	}{
		{"positional arguments", toolsHeader(`    Tool("ALLOY", "grafana/alloy", "v{version}", {"AMD64": "a.zip", "ARM64": "b.zip"}, "SHA256SUMS"),`), "ALLOY"},
		{"keyword arguments and no checksums", toolsHeader(`    Tool(prefix="GH", repo="cli/cli", tag="v{version}", assets={"AMD64": "gh.tgz"}),`), "GH"},
		{"checksums given as None", toolsHeader(`    Tool("GH", "cli/cli", "v{version}", {"AMD64": "gh.tgz"}, None),`), "GH"},
		{"a list of tools, two of them", "TOOLS = [\n Tool('A', 'o/r', 't', {'X': 'n'}),\n Tool('B', 'o/r', 't', {'X': 'n'}),\n]\n", "A B"},
		{"code beside TOOLS is not run", "import os\nos.system('touch /nonexistent/x')\n" + toolsHeader(`    Tool("A", "o/r", "t", {"X": "n"}),`), "A"},
		{"comments and continuation lines", "TOOLS = (  # the tools\n    Tool(\n        'A',  # prefix\n        'o/r', 't',\n        {'X': 'n'},\n    ),\n)\n", "A"},
	}
	for _, c := range cases {
		var order []string
		if line := core.Guard(func() { _, order = ParseTools(c.text) }); line != "" {
			t.Errorf("%s: refused: %s", c.label, line)
		} else if got := strings.Join(order, " "); got != c.prefixes {
			t.Errorf("%s: read %q, want %q", c.label, got, c.prefixes)
		}
	}
	tools, _ := ParseTools(toolsHeader(`    Tool("A", "o/r", "v{version}", {"X": "n", "Y": "m"}, "SUMS"),`))
	a := tools["A"]
	if a.Repo != "o/r" || a.Tag != "v{version}" || len(a.Assets) != 2 || a.Assets[1].Arch != "Y" || a.Checksums == nil || *a.Checksums != "SUMS" {
		t.Errorf("a tool is read as written: %+v", a)
	}
}

func TestParseToolsRefuses(t *testing.T) {
	ok := `"A", "o/r", "t", {"X": "n"}`
	cases := []struct{ label, text, want string }{
		{"no TOOLS", "x = 1\n", "has no top-level TOOLS tuple"},
		{"TOOLS that is not a sequence", "TOOLS = {}\n", "has no top-level TOOLS tuple"},
		{"an empty TOOLS", "TOOLS = ()\n", "names no tool"},
		{"something other than a Tool call", "TOOLS = (Other('A'),)\n", "holds something other than a Tool(...) call"},
		{"a call that is not a name", "TOOLS = (a.Tool('A'),)\n", "holds something other than a Tool(...) call"},
		{"too many positional arguments", toolsHeader(`    Tool(` + ok + `, "s", "extra"),`), "too many positional arguments"},
		{"an unexpected keyword", toolsHeader(`    Tool(` + ok + `, evil="x"),`), "unexpected argument"},
		{"an argument given twice", toolsHeader(`    Tool(` + ok + `, prefix="B"),`), "unexpected argument"},
		{"a missing required field", toolsHeader(`    Tool("A", "o/r", "t"),`), "missing a required field"},
		{"a lowercase prefix", toolsHeader(`    Tool("a", "o/r", "t", {"X": "n"}),`), "not an uppercase identifier"},
		{"a prefix that is not a string", toolsHeader(`    Tool(1, "o/r", "t", {"X": "n"}),`), "not an uppercase identifier"},
		{"a repository that is not owner/repo", toolsHeader(`    Tool("A", "o/r/extra", "t", {"X": "n"}),`), "is not owner/repo"},
		{"a repository with a leading dot", toolsHeader(`    Tool("A", "../r", "t", {"X": "n"}),`), "is not owner/repo"},
		{"a tag that is not a string", toolsHeader(`    Tool("A", "o/r", 1, {"X": "n"}),`), "release tag template for A"},
		{"no assets", toolsHeader(`    Tool("A", "o/r", "t", {}),`), "asset map for A"},
		{"assets that are not a mapping", toolsHeader(`    Tool("A", "o/r", "t", ["X"]),`), "asset map for A"},
		{"an architecture in lowercase", toolsHeader(`    Tool("A", "o/r", "t", {"x": "n"}),`), "asset map for A"},
		{"an asset name that is not a string", toolsHeader(`    Tool("A", "o/r", "t", {"X": 1}),`), "asset map for A"},
		{"checksums that are not a string", toolsHeader(`    Tool("A", "o/r", "t", {"X": "n"}, 1),`), "checksums-file template for A"},
		{"the same prefix twice", toolsHeader(`    Tool(` + ok + `),` + "\n" + `    Tool(` + ok + `),`), "names A more than once"},
		{"a variable as an argument", toolsHeader(`    Tool(PREFIX, "o/r", "t", {"X": "n"}),`), "not a literal"},
		{"a call as an argument", toolsHeader(`    Tool("A", "o/r", __import__("os").getcwd(), {"X": "n"}),`), "not a literal"},
		{"a format string as an argument", toolsHeader(`    Tool("A", "o/r", f"{x}", {"X": "n"}),`), "not a literal"},
		{"an unbalanced bracket", "TOOLS = (Tool('A', 'o/r', 't', {'X': 'n'})\n", "does not parse"},
	}
	for _, c := range cases {
		line := core.Guard(func() { ParseTools(c.text) })
		if !strings.HasPrefix(line, "FAIL: ") || !strings.Contains(line, c.want) {
			t.Errorf("%s: %q, want a refusal with %q", c.label, line, c.want)
		}
	}
}

var releaseNames = []asset{{"AMD64", "tool-amd64.zip"}, {"ARM64", "tool-arm64.zip"}}

func releaseJSON(assets string) []byte { return []byte(`{"assets": [` + assets + `]}`) }

func TestFromRelease(t *testing.T) {
	good := `{"name": "tool-amd64.zip", "digest": "sha256:` + strings.ToUpper(shaA) + `"}, {"name": "tool-arm64.zip", "digest": "sha256:` + shaB + `"}, {"name": "other.zip", "digest": 5}`
	got := map[string]string{}
	if line := core.Guard(func() { got = fromRelease(releaseJSON(good), releaseNames, "the release") }); line != "" {
		t.Fatalf("an honest release is refused: %s", line)
	}
	if got["AMD64"] != shaA || got["ARM64"] != shaB {
		t.Errorf("digests are read lowercase, by architecture: %v", got)
	}
	amd := `{"name": "tool-amd64.zip", "digest": "sha256:` + shaA + `"}`
	arm := `{"name": "tool-arm64.zip", "digest": "sha256:` + shaB + `"}`
	cases := []struct {
		label string
		data  []byte
		want  string
	}{
		{"not JSON", []byte("<html>"), "is not JSON"},
		{"not UTF-8", []byte("{\"assets\": [], \"x\": \"\xff\"}"), "is not JSON"},
		{"data after the document", []byte(`{"assets": []} []`), "is not JSON"},
		{"a list", []byte(`[]`), "is not a release"},
		{"assets that are not a list", []byte(`{"assets": {}}`), "is not a release"},
		{"an asset that is not an object", releaseJSON(amd + `, ` + arm + `, 5`), "is not a release"},
		{"an asset that is missing", releaseJSON(amd), `exactly one asset tool-arm64.zip`},
		{"an asset listed twice", releaseJSON(amd + `, ` + amd + `, ` + arm), `exactly one asset tool-amd64.zip`},
		{"a digest that is not a string", releaseJSON(amd + `, {"name": "tool-arm64.zip", "digest": 5}`), "exactly one asset tool-arm64.zip"},
		{"a digest without the sha256 prefix", releaseJSON(amd + `, {"name": "tool-arm64.zip", "digest": "md5:` + shaB + `"}`), "exactly one asset tool-arm64.zip"},
		{"a digest that is too short", releaseJSON(amd + `, {"name": "tool-arm64.zip", "digest": "sha256:abc"}`), "malformed digest"},
		{"a digest that is not hexadecimal", releaseJSON(amd + `, {"name": "tool-arm64.zip", "digest": "sha256:` + strings.Repeat("z", 64) + `"}`), "malformed digest"},
		{"a digest with trailing text", releaseJSON(amd + `, {"name": "tool-arm64.zip", "digest": "sha256:` + shaB + `\n"}`), "malformed digest"},
	}
	for _, c := range cases {
		line := core.Guard(func() { fromRelease(c.data, releaseNames, "the release") })
		if !strings.HasPrefix(line, "FAIL: ") || !strings.Contains(line, c.want) {
			t.Errorf("%s: %q, want a refusal with %q", c.label, line, c.want)
		}
	}
}

func TestFromChecksumFile(t *testing.T) {
	file := shaA + "  tool-amd64.zip\n" + strings.ToUpper(shaB) + " *tool-arm64.zip\n" + shaC + "  other.zip\n"
	var got map[string]string
	if line := core.Guard(func() { got = fromChecksumFile([]byte(file), releaseNames, "SHA256SUMS") }); line != "" {
		t.Fatalf("an honest checksums file is refused: %s", line)
	}
	if got["AMD64"] != shaA || got["ARM64"] != shaB {
		t.Errorf("digests are read lowercase, by architecture: %v", got)
	}
	if line := core.Guard(func() {
		got = fromChecksumFile([]byte(shaA+"  ./tool-amd64.zip\r\n"+shaB+"\ttool-arm64.zip"), releaseNames, "SHA256SUMS")
	}); line != "" || got["AMD64"] != shaA {
		t.Errorf("a ./ prefix, CRLF and a tab separator are read: %q %v", line, got)
	}
	cases := []struct {
		label string
		data  []byte
		want  string
	}{
		{"not UTF-8", []byte("\xff"), "is not UTF-8"},
		{"an empty file", []byte(""), `exactly one sha256 line for tool-amd64.zip, found 0`},
		{"a name missing", []byte(shaA + "  tool-amd64.zip\n"), `exactly one sha256 line for tool-arm64.zip, found 0`},
		{"a name listed twice", []byte(shaA + "  tool-amd64.zip\n" + shaC + "  tool-amd64.zip\n" + shaB + "  tool-arm64.zip\n"), `for tool-amd64.zip, found 2`},
		{"a digest that is too short", []byte("abc  tool-amd64.zip\n" + shaB + "  tool-arm64.zip\n"), `exactly one sha256 line for tool-amd64.zip`},
		{"a name that only ends like the asset", []byte(shaA + "  evil-tool-amd64.zip\n" + shaB + "  tool-arm64.zip\n"), `for tool-amd64.zip, found 0`},
		{"a line with three fields", []byte(shaA + "  tool-amd64.zip  extra\n" + shaB + "  tool-arm64.zip\n"), `for tool-amd64.zip, found 0`},
		{"a name in a directory", []byte(shaA + "  dist/tool-amd64.zip\n" + shaB + "  tool-arm64.zip\n"), `for tool-amd64.zip, found 0`},
	}
	for _, c := range cases {
		line := core.Guard(func() { fromChecksumFile(c.data, releaseNames, "SHA256SUMS") })
		if !strings.HasPrefix(line, "FAIL: ") || !strings.Contains(line, c.want) {
			t.Errorf("%s: %q, want a refusal with %q", c.label, line, c.want)
		}
	}
}

func TestVersionOrder(t *testing.T) {
	cases := []struct{ older, newer string }{
		{"1.21.2", "1.21.3"},
		{"1.21.2", "1.21.10"},
		{"1.9.0", "1.10.0"},
		{"1.21.2", "2.0.0"},
		{"2.0.0-rc.1", "2.0.0"},
		{"2.0.0-rc.1", "2.0.0-rc.2"},
		{"2.0.0-rc.2", "2.0.0-rc.10"},
		{"2.0.0-alpha", "2.0.0-beta"},
		{"2.0.0-alpha", "2.0.0-alpha.1"},
		{"2.0.0-alpha.1", "2.0.0-alpha.beta"},
		{"2.0.0-alpha.beta", "2.0.0-beta"},
		{"2.0.0-beta.2", "2.0.0-beta.11"},
		{"2.0.0-rc9", "2.0.0-rc10"},
		{"1.2.3rc1", "1.2.3rc2"},
		{"1.2.3-1", "1.2.3-2"},
		{"1.2.3-0.3.7", "1.2.3-0.3.8"},
		{"1.2.3_rc1", "1.2.3_rc2"},
		{"1.2.3.rc1", "1.2.3.rc2"},
		{"2.0.0-rc.1", "2.0.1-rc.1"},
		{"v1.2.3", "v1.2.4"},
		{"1.2", "1.2.1"},
		{"1.0.0", "2.0.0-rc.1"},
		{"2024-01-15", "2024-02-01"},
		{"r2024a", "r2024b"},
		{"build9", "build10"},
	}
	for _, c := range cases {
		if got := compareVersions(c.newer, c.older); got != 1 {
			t.Errorf("%s must be newer than %s: %d", c.newer, c.older, got)
		}
		if got := compareVersions(c.older, c.newer); got != -1 {
			t.Errorf("%s must be older than %s: %d", c.older, c.newer, got)
		}
		reject := func(oldV, newV string) string {
			return core.Guard(func() { rejectIfDowngrade("TOOL", oldV, newV) })
		}
		if got := reject(c.older, c.newer); got != "" {
			t.Errorf("%s to %s is an update: %s", c.older, c.newer, got)
		}
		if got := reject(c.newer, c.older); !strings.Contains(got, "a downgrade is not an update") {
			t.Errorf("%s to %s is a downgrade: %q", c.newer, c.older, got)
		}
	}
	for _, same := range [][2]string{{"1.2.3", "1.2.3"}, {"1.2", "1.2.0"}, {"1.02.3", "1.2.3"}, {"v1.2.3", "1.2.3"},
		{"1.2.3+build.1", "1.2.3"}, {"1.2.3-rc.1+a", "1.2.3-rc.1+b"}, {"1.2.3rc1", "1.2.3-rc1"}, {"1.2.3_rc1", "1.2.3~rc1"}, {"1.2.3.rc1", "1.2.3-rc1"}} {
		if got := compareVersions(same[0], same[1]); got != 0 {
			t.Errorf("%s and %s are one version: %d", same[0], same[1], got)
		}
	}
}

func TestPathIsQuotedInEveryMessage(t *testing.T) {
	tools, _ := ParseTools(testMapping)
	hostile := ".github/tool.env\n::error::forged\x1b[31m" + strings.Repeat("x", 200) + ".env"
	fetch := func(*Tool, string, string, *string, []string) map[string]string {
		return map[string]string{"AMD64": shaB, "ARM64": shaD}
	}
	base := pins(envBaseTemplate)
	bump := func(text string) string {
		return strings.Replace(text, "ALLOY_VERSION=1.21.2", "ALLOY_VERSION=1.21.3", 1)
	}
	withShas := strings.NewReplacer("ALLOY_SHA256_AMD64="+shaA, "ALLOY_SHA256_AMD64="+shaB, "ALLOY_SHA256_ARM64="+shaC, "ALLOY_SHA256_ARM64="+shaD)
	head := withShas.Replace(bump(base))
	cases := []struct {
		label, base, head string
	}{
		{"a line gained", base, head + "X=1\n"},
		{"an unrecognised line", base, strings.Replace(head, "GH_VERSION=2.0.0", "GH_VERSION=oops", 1)},
		{"two versions", base, strings.Replace(head, "GH_VERSION=2.0.0", "GH_VERSION=2.0.1", 1)},
		{"no checksum", base, bump(base)},
		{"another tool's checksum", base, strings.Replace(bump(base), "GH_SHA256_AMD64="+shaA, "GH_SHA256_AMD64="+shaB, 1)},
		{"a checksum that is not a digest", base, strings.Replace(bump(base), "ALLOY_SHA256_AMD64="+shaA, "ALLOY_SHA256_AMD64="+strings.ToUpper(shaA), 1)},
		{"a digest upstream does not publish", base, strings.Replace(head, shaD, shaC, 1)},
	}
	for _, c := range cases {
		line := core.Guard(func() { check(tools, hostile, c.base, c.head, fetch) })
		if line == "" || strings.ContainsAny(line, "\n\x1b") || strings.Contains(line, strings.Repeat("x", 100)) {
			t.Errorf("%s: the path must be quoted in %q", c.label, line)
		}
	}
}

// A tilde is not a character of a plain version token, so a version carrying one is refused as a
// token before its order matters; the comparison still reads it as a prerelease.
func TestTildeIsAPrerelease(t *testing.T) {
	if compareVersions("1.2.3~rc1", "1.2.3") != -1 || compareVersions("1.2.3~rc1", "1.2.3~rc2") != -1 || compareVersions("1.2.3~rc1", "1.2.3-rc1") != 0 {
		t.Errorf("a tilde starts a prerelease")
	}
	tools, _ := ParseTools(testMapping)
	base := pins(envBaseTemplate)
	head := strings.NewReplacer("ALLOY_VERSION=1.21.2", "ALLOY_VERSION=1.21.3~rc1", "ALLOY_SHA256_AMD64="+shaA, "ALLOY_SHA256_AMD64="+shaB).Replace(base)
	got := core.Guard(func() {
		check(tools, ".github/tool.env", base, head, func(*Tool, string, string, *string, []string) map[string]string {
			return map[string]string{"AMD64": shaB}
		})
	})
	if !strings.Contains(got, "is not a plain version token") {
		t.Errorf("a tilde version is refused: %q", got)
	}
}

func TestPrereleasesAreDowngrades(t *testing.T) {
	for _, c := range []struct{ release, pre string }{
		{"1.0.0", "1.0.0-1"},
		{"1.0.0", "1.0.0-0.3.7"},
		{"1.2.3", "1.2.3rc1"},
		{"1.2.3", "1.2.3.rc1"},
		{"1.2.3", "1.2.3_rc1"},
		{"1.2.3", "1.2.3-rc.1"},
		{"2.0.0", "2.0.0-alpha.beta"},
		{"1.2", "1.2.0-rc1"},
		{"v1.2.3", "v1.2.3-rc1"},
	} {
		got := core.Guard(func() { rejectIfDowngrade("TOOL", c.release, c.pre) })
		if !strings.Contains(got, "a downgrade is not an update") {
			t.Errorf("%s to %s is a downgrade: %q", c.release, c.pre, got)
		}
		if got := core.Guard(func() { rejectIfDowngrade("TOOL", c.pre, c.release) }); got != "" {
			t.Errorf("%s to %s is an update: %q", c.pre, c.release, got)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestAVerdictThatCannotBeWrittenIsNotAPass(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mapping := write("checksums.py", testMapping)
	base := write("base", pins(envBaseTemplate))
	// No change at all fails (no version moves), so the refusal path is the one tried first.
	if code := Main([]string{mapping, ".github/tool.env", base, base}, failingWriter{}); code != 1 {
		t.Errorf("a refusal that cannot be written exits %d", code)
	}
	if code := Main(nil, failingWriter{}); code != 1 {
		t.Errorf("a usage line that cannot be written exits %d", code)
	}
}
