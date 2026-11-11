package checksumcheck

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type answer struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// TestMainWithRecordedAnswers drives the whole check, digests from upstream included, through
// recorded answers: the upstream hosts are never contacted.
func TestMainWithRecordedAnswers(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mapping := write("checksums.py", testMapping)
	const alloySums = "https://github.com/grafana/alloy/releases/download/v1.21.3/SHA256SUMS"
	const ghRelease = "https://api.github.com/repos/cli/cli/releases/tags/v2.0.1"
	const rubyRelease = "https://api.github.com/repos/ruby/ruby-builder/releases/tags/ruby-3.3.0"
	sums := func(amd, arm string) answer {
		return answer{200, amd + "  alloy-linux-amd64.zip\n" + arm + "  alloy-linux-arm64.zip\n"}
	}
	release := func(name, digest string) answer {
		return answer{200, `{"assets": [{"name": "` + name + `", "digest": "sha256:` + digest + `"}]}`}
	}
	recorded := func(m map[string]answer) string {
		data, _ := json.Marshal(m)
		return write("urls.json", string(data))
	}

	base := pins(envBaseTemplate)
	alloyHead := strings.NewReplacer("ALLOY_VERSION=1.21.2", "ALLOY_VERSION=1.21.3",
		"ALLOY_SHA256_AMD64="+shaA, "ALLOY_SHA256_AMD64="+shaB, "ALLOY_SHA256_ARM64="+shaC, "ALLOY_SHA256_ARM64="+shaD).Replace(base)
	ghHead := strings.NewReplacer("GH_VERSION=2.0.0", "GH_VERSION=2.0.1", "GH_SHA256_AMD64="+shaA, "GH_SHA256_AMD64="+shaB).Replace(base)
	rubyBase := base + "RUBY_UBUNTU_VERSION=24.04\nRUBY_VERSION=3.2.0\nRUBY_SHA256_AMD64=" + shaA + "\n"
	rubyHead := strings.NewReplacer("RUBY_VERSION=3.2.0", "RUBY_VERSION=3.3.0", "RUBY_SHA256_AMD64="+shaA, "RUBY_SHA256_AMD64="+shaB).Replace(rubyBase)

	cases := []struct {
		label, base, head string
		urls              map[string]answer
		want              string
	}{
		{"a checksums file upstream, both digests", base, alloyHead, map[string]answer{alloySums: sums(shaB, shaD)}, ""},
		{"a release with a digest per asset", base, ghHead, map[string]answer{ghRelease: release("gh_2.0.1_linux_amd64.tar.gz", shaB)}, ""},
		{"{ubuntu} filled from the pin line", rubyBase, rubyHead, map[string]answer{rubyRelease: release("ruby-3.3.0-ubuntu-24.04-x64.tar.gz", shaB)}, ""},
		{"a digest upstream does not publish", base, alloyHead, map[string]answer{alloySums: sums(shaB, shaC)}, "to a digest that is not the one upstream publishes"},
		{"a checksums file with an architecture missing", base, alloyHead, map[string]answer{alloySums: {200, shaB + "  alloy-linux-amd64.zip\n"}}, "found 0"},
		{"an answer that is not a 200", base, alloyHead, map[string]answer{alloySums: {404, "gone"}}, "answered 404"},
		{"an answer nobody recorded", base, alloyHead, map[string]answer{}, "no recorded answer"},
		{"a release whose digest is for another asset", base, ghHead, map[string]answer{ghRelease: release("gh_2.0.1_linux_arm64.tar.gz", shaB)}, "must list exactly one asset"},
		{"a release that is not JSON", base, ghHead, map[string]answer{ghRelease: {200, "<html>"}}, "is not JSON"},
		{"{ubuntu} with another release asset", rubyBase, rubyHead, map[string]answer{rubyRelease: release("ruby-3.3.0-ubuntu-22.04-x64.tar.gz", shaB)}, "must list exactly one asset"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		code := Main([]string{"--urls", recorded(c.urls), mapping, ".github/tool.env", write("base", c.base), write("head", c.head)}, &out)
		line := strings.TrimSpace(out.String())
		switch {
		case c.want == "" && (code != 0 || line != "PASS"):
			t.Errorf("%s: %d %q, want PASS", c.label, code, line)
		case c.want != "" && (code != 1 || !strings.HasPrefix(line, "FAIL: ") || !strings.Contains(line, c.want)):
			t.Errorf("%s: %d %q, want a refusal with %q", c.label, code, line, c.want)
		}
	}

	// A verdict that cannot be written is not a pass, whatever the verdict is.
	good := cases[0]
	goodArgs := []string{"--urls", recorded(good.urls), mapping, ".github/tool.env", write("base", good.base), write("head", good.head)}
	var out bytes.Buffer
	if code := Main(goodArgs, &out); code != 0 || strings.TrimSpace(out.String()) != "PASS" {
		t.Fatalf("the control passes: %d %q", code, out.String())
	}
	if code := Main(goodArgs, failingWriter{}); code != 1 {
		t.Errorf("a PASS that could not be written exits %d", code)
	}

	// Every file named on the command line is read inside its directory.
	for i, name := range []string{"urls.json", "checksums.py", "base", "head"} {
		outside := filepath.Join(t.TempDir(), name)
		if err := os.Rename(filepath.Join(dir, name), outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
		out.Reset()
		code := Main(goodArgs, &out)
		if line := strings.TrimSpace(out.String()); code != 1 || !strings.HasPrefix(line, "FAIL: ") {
			t.Errorf("file %d (%s) as a link out of its directory: %d %q", i, name, code, line)
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(outside, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if code := Main(goodArgs, &out); code != 0 {
		t.Errorf("the files are back and pass again: %d", code)
	}
}
