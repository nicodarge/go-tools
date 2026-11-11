package checksumcheck

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/go-tools/internal/core"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func respond(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header}
}

// refusal runs fn and returns the `FAIL: ...` line it ends in; fn not refusing is a test failure.
func refusal(t *testing.T, fn func()) string {
	t.Helper()
	line := core.Guard(fn)
	if line == "" {
		t.Fatalf("expected a refusal")
	}
	return line
}

func TestDownloadCap(t *testing.T) {
	defer func(old http.RoundTripper) { httpTransport = old }(httpTransport)

	serve := func(n int) {
		httpTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return respond(200, strings.Repeat("x", n), nil), nil
		})
	}
	serve(maxDownloadBytes)
	if status, data := urlopen(nil, "https://api.github.com/x", time.Second, nil); status != 200 || len(data) != maxDownloadBytes {
		t.Fatalf("a response exactly at the cap is read in full: %d %d", status, len(data))
	}
	serve(maxDownloadBytes + 1)
	msg := refusal(t, func() { urlopen(nil, "https://api.github.com/x", time.Second, nil) })
	if !strings.Contains(msg, "larger than the limit") {
		t.Fatalf("one byte over the cap must be rejected, not truncated: %q", msg)
	}
}

func TestRedirects(t *testing.T) {
	defer func(old http.RoundTripper) { httpTransport = old }(httpTransport)
	hops := []string{}
	redirectTo := func(location string) {
		httpTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			hops = append(hops, r.URL.String())
			if r.URL.Host == "github.com" || r.URL.Host == "api.github.com" {
				return respond(302, "", http.Header{"Location": {location}}), nil
			}
			if r.URL.Host == "release-assets.githubusercontent.com" {
				return respond(200, "asset", nil), nil
			}
			return respond(302, "", http.Header{"Location": {"https://evil.example/x"}}), nil
		})
	}
	redirectTo("https://release-assets.githubusercontent.com/x")
	if status, body := urlopen(nil, "https://github.com/a/b", time.Second, nil); status != 200 || string(body) != "asset" {
		t.Fatalf("a hop from github.com to the asset host is followed: %d %q", status, body)
	}
	for _, location := range []string{
		"https://evil-githubusercontent.com.evil/x",
		"https://evil.example/x",
		"http://release-assets.githubusercontent.com/x",
	} {
		hops = nil
		redirectTo(location)
		if status, _ := urlopen(nil, "https://github.com/a/b", time.Second, nil); status != 302 {
			t.Fatalf("a redirect to %s must be refused, got status %d", location, status)
		}
		if len(hops) != 1 {
			t.Fatalf("a refused redirect must not be requested: %v", hops)
		}
	}
	// A second hop, from the asset host onward, is refused.
	hops = nil
	httpTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hops = append(hops, r.URL.Host)
		if r.URL.Host == "github.com" {
			return respond(302, "", http.Header{"Location": {"https://release-assets.githubusercontent.com/x"}}), nil
		}
		return respond(302, "", http.Header{"Location": {"https://evil.example/x"}}), nil
	})
	if status, _ := urlopen(nil, "https://github.com/a/b", time.Second, nil); status != 302 || len(hops) != 2 {
		t.Fatalf("a second hop must be refused: %d %v", status, hops)
	}
}

func TestHostAllowlist(t *testing.T) {
	msg := refusal(t, func() { urlopen(nil, "https://evil.example/x", time.Second, nil) })
	if !strings.Contains(msg, "host is") {
		t.Fatalf("a disallowed host is refused before any request: %q", msg)
	}
}

func TestPyFormat(t *testing.T) {
	fields := map[string]string{"version": "1.2.3"}
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"v{version}", "v1.2.3", true},
		{"{{literal}}", "{literal}", true},
		{"ruby-{version}-{ubuntu}", "", false},
		{"{}", "", false},
		{"{0}", "", false},
	}
	for _, c := range cases {
		got, ok := pyFormat(c.in, fields)
		if ok != c.ok || got != c.want {
			t.Errorf("pyFormat(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestPyQuote(t *testing.T) {
	if got := pyQuote("v1.2.3/../ev il~"); got != "v1.2.3/../ev%20il~" {
		t.Fatalf("pyQuote: %q", got)
	}
}

func TestSplitLines(t *testing.T) {
	got := splitLines("a\r\nb\rc\x0bd\u2028e\n")
	if strings.Join(got, "|") != "a|b|c|d|e" {
		t.Fatalf("splitLines: %q", got)
	}
}

const testMapping = `
TOOLS = (
    Tool(
        "ALLOY",
        "grafana/alloy",
        "v{version}",
        {"AMD64": "alloy-linux-amd64.zip", "ARM64": "alloy-linux-arm64.zip"},
        "SHA256SUMS",
    ),
    Tool(
        "GH",
        "cli/cli",
        "v{version}",
        {"AMD64": "gh_{version}_linux_amd64.tar.gz", "ARM64": "gh_{version}_linux_arm64.tar.gz"},
    ),
    Tool(
        "RUBY",
        "ruby/ruby-builder",
        "ruby-{version}",
        {"AMD64": "ruby-{version}-ubuntu-{ubuntu}-x64.tar.gz"},
    ),
)
`

var (
	shaA = strings.Repeat("a", 64)
	shaB = strings.Repeat("b", 64)
	shaC = strings.Repeat("c", 64)
	shaD = strings.Repeat("d", 64)
)

const envBaseTemplate = `# renovate: datasource=github-releases depName=grafana/alloy extractVersion=^v(?<version>.+)
ALLOY_VERSION=1.21.2
ALLOY_SHA256_AMD64=A
ALLOY_SHA256_ARM64=C
GH_VERSION=2.0.0
GH_SHA256_AMD64=A
`

func pins(template string) string {
	r := strings.NewReplacer("=A\n", "="+shaA+"\n", "=B\n", "="+shaB+"\n", "=C\n", "="+shaC+"\n", "=D\n", "="+shaD+"\n")
	return r.Replace(template)
}

// asDockerfile gives the pin lines of an env file their `ARG ` lead.
func asDockerfile(text string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "_VERSION=") || strings.Contains(line, "_SHA256_") {
			line = "ARG " + line
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func TestPinFiles(t *testing.T) {
	tools, _ := ParseTools(testMapping)
	base := pins(envBaseTemplate)
	head := pins(strings.NewReplacer("ALLOY_VERSION=1.21.2", "ALLOY_VERSION=1.21.3",
		"ALLOY_SHA256_AMD64=A", "ALLOY_SHA256_AMD64=B", "ALLOY_SHA256_ARM64=C", "ALLOY_SHA256_ARM64=D").Replace(envBaseTemplate))
	digests := map[string]string{"AMD64": shaB, "ARM64": shaD}
	fetch := func(*Tool, string, string, *string, []string) map[string]string { return digests }
	run := func(path, baseText, headText string) string {
		return core.Guard(func() { check(tools, path, baseText, headText, fetch) })
	}
	edit := func(text, old, replacement string) string {
		if !strings.Contains(text, old) {
			t.Fatalf("%q not in the fixture", old)
		}
		return strings.Replace(text, old, replacement, 1)
	}
	const notAPinFile = "is neither a Dockerfile nor a tool-pin env file"
	cases := []struct {
		label, path, base, head, want string
	}{
		{"an env file: a real two-architecture bump", ".github/tool.env", base, head, ""},
		{"an env file: another name ending in .env", "tools/pins.env", base, head, ""},
		{"a Dockerfile: the same bump with ARG lines", "tools/Dockerfile", asDockerfile(base), asDockerfile(head), ""},
		{"an env file: only one architecture changed", ".github/tool.env", base,
			edit(edit(base, "ALLOY_VERSION=1.21.2", "ALLOY_VERSION=1.21.3"), "ALLOY_SHA256_AMD64="+shaA, "ALLOY_SHA256_AMD64="+shaB), ""},
		{"an env file: the renovate comment changes", ".github/tool.env", base,
			edit(head, "depName=grafana/alloy", "depName=evil/alloy"), "does not recognise"},
		{"an env file: the file gains a line", ".github/tool.env", base, head + "EVIL=1\n", "gains or loses a line"},
		{"an env file: two tools bumped at once", ".github/tool.env", base,
			edit(head, "GH_VERSION=2.0.0", "GH_VERSION=2.0.1"), "exactly one must change"},
		{"an env file: a quoted version", ".github/tool.env", base,
			edit(head, "ALLOY_VERSION=1.21.3", `ALLOY_VERSION="1.21.3"`), "is not a plain version token"},
		{"an env file: a version that runs a command when sourced", ".github/tool.env", base,
			edit(head, "ALLOY_VERSION=1.21.3", "ALLOY_VERSION=1.21.3;curl${IFS}evil"), "is not a plain version token"},
		{"an env file: a digest in quotes", ".github/tool.env", base,
			edit(head, "ALLOY_SHA256_AMD64="+shaB, `ALLOY_SHA256_AMD64="`+shaB+`"`), "does not recognise"},
		{"an env file: a digest followed by a command", ".github/tool.env", base,
			edit(head, "ALLOY_SHA256_AMD64="+shaB, "ALLOY_SHA256_AMD64="+shaB+";id"), "does not recognise"},
		{"an env file: a digest followed by a space", ".github/tool.env", base,
			edit(head, "ALLOY_SHA256_AMD64="+shaB, "ALLOY_SHA256_AMD64="+shaB+" "), "does not recognise"},
		{"an env file: pin lines behind an export", ".github/tool.env",
			strings.ReplaceAll(base, "ALLOY_VERSION=", "export ALLOY_VERSION="),
			strings.ReplaceAll(head, "ALLOY_VERSION=", "export ALLOY_VERSION="), "does not recognise"},
		{"an env file: Dockerfile ARG lines are not its pin lines", ".github/tool.env", asDockerfile(base), asDockerfile(head), "does not recognise"},
		{"a Dockerfile: env-style pin lines are not its pin lines", "tools/Dockerfile", base, head, "does not recognise"},
		{"a tampered digest", ".github/tool.env", base,
			edit(head, "ALLOY_SHA256_AMD64="+shaB, "ALLOY_SHA256_AMD64="+shaC), "is not the one upstream publishes"},
		{"a shell script", ".github/tool.sh", base, head, notAPinFile},
		{"a name ending in .env.bak", ".github/tool.env.bak", base, head, notAPinFile},
		{"a name that is only .env", ".env", base, head, notAPinFile},
		{"a Dockerfile.bak", "tools/Dockerfile.bak", asDockerfile(base), asDockerfile(head), notAPinFile},
		{"a Dockerfile in another case", "dockerfile", asDockerfile(base), asDockerfile(head), notAPinFile},
	}
	for _, c := range cases {
		got := run(c.path, c.base, c.head)
		switch {
		case c.want == "" && got != "":
			t.Errorf("%s: refused: %s", c.label, got)
		case c.want != "" && !strings.Contains(got, c.want):
			t.Errorf("%s: %q, want a refusal containing %q", c.label, got, c.want)
		}
	}

	rubyBase := base + "RUBY_UBUNTU_VERSION=24.04\nRUBY_VERSION=3.2.0\nRUBY_SHA256_AMD64=" + shaA + "\n"
	rubyHead := edit(edit(rubyBase, "RUBY_VERSION=3.2.0", "RUBY_VERSION=3.3.0"), "RUBY_SHA256_AMD64="+shaA, "RUBY_SHA256_AMD64="+shaB)
	digests = map[string]string{"AMD64": shaB}
	if got := run(".github/tool.env", rubyBase, rubyHead); got != "" {
		t.Errorf("an env file: {ubuntu} is read from RUBY_UBUNTU_VERSION without the ARG: %s", got)
	}
	noUbuntu := func(text string) string { return strings.Replace(text, "RUBY_UBUNTU_VERSION=24.04\n", "", 1) }
	if got := run(".github/tool.env", noUbuntu(rubyBase), noUbuntu(rubyHead)); !strings.Contains(got, "needs RUBY_UBUNTU_VERSION") {
		t.Errorf("an env file: a Ruby bump with no RUBY_UBUNTU_VERSION: %q", got)
	}
}
