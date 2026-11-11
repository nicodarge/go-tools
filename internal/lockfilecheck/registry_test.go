package lockfilecheck

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicodarge/go-tools/internal/core"
)

var (
	intA = "sha512-" + strings.Repeat("A", 86) + "=="
	intB = "sha512-" + strings.Repeat("B", 86) + "=="
)

func lockText(version, integrity string) []byte {
	return []byte(fmt.Sprintf(`{"name":"demo","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{`+
		`"":{"name":"demo","version":"1.0.0","dependencies":{"foo":"^1.0.0"}},`+
		`"node_modules/foo":{"version":"%s","resolved":"%s/foo/-/foo-%s.tgz","integrity":"%s"}}}`,
		version, registryURL, version, integrity))
}

const longAgo = "2020-01-01T00:00:00.000Z"

type fakeNPM struct {
	mu    sync.Mutex
	pages map[string]string // escaped path -> body
	seen  []string
	hook  func(w http.ResponseWriter, r *http.Request) bool
}

func (f *fakeNPM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.URL.EscapedPath())
	f.mu.Unlock()
	if f.hook != nil && f.hook(w, r) {
		return
	}
	body, ok := f.pages[r.URL.EscapedPath()]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(body))
}

func honestPages() map[string]string {
	return map[string]string{
		"/foo/1.0.1": fmt.Sprintf(`{"name":"foo","version":"1.0.1","dist":{"integrity":%q}}`, intB),
		"/foo": fmt.Sprintf(`{"name":"foo","time":{"1.0.0":%q,"1.0.1":%q},"versions":{"1.0.0":{},"1.0.1":{}}}`,
			longAgo, longAgo),
	}
}

// run checks a bump of foo against a registry served by f, with the given request bounds.
func run(t *testing.T, f *fakeNPM, perRequest, budget time.Duration, limit int) string {
	t.Helper()
	line, _ := timedRun(t, f, perRequest, budget, limit)
	return line
}

// timedRun also returns how long the check took, not counting the server shutting down.
func timedRun(t *testing.T, f *fakeNPM, perRequest, budget time.Duration, limit int) (string, time.Duration) {
	t.Helper()
	srv := httptest.NewServer(f)
	defer srv.Close()
	saved := registryURL
	registryURL = srv.URL
	defer func() { registryURL = saved }()
	before := "2030-01-01T00:00:00Z"
	started := time.Now()
	line := core.Guard(func() {
		Check("package-lock.json", lockText("1.0.0", intA), lockText("1.0.1", intB),
			newHTTPRegistry(perRequest, budget, limit, 4), &before)
	})
	return line, time.Since(started)
}

func TestRegistryAnswers(t *testing.T) {
	f := &fakeNPM{pages: honestPages()}
	if line := run(t, f, 2*time.Second, 10*time.Second, 300); line != "" {
		t.Fatalf("every answer a 200 passes: %s", line)
	}
	asked := strings.Join(f.seen, " ")
	for _, want := range []string{"/foo/1.0.1", "/foo"} {
		if !strings.Contains(asked, want) {
			t.Errorf("%s must be asked for: %s", want, asked)
		}
	}
}

func TestRegistryScopedNameUsesEscapedSlash(t *testing.T) {
	if got := NewHTTPRegistry().url(pair{name: "@types/node", version: "24.19.2"}); !strings.HasSuffix(got, "/@types%2Fnode/24.19.2") {
		t.Fatalf("a scoped name is asked for with its slash as %%2F: %s", got)
	}
	if got := NewHTTPRegistry().url(pair{name: "@types/node", isPack: true}); !strings.HasSuffix(got, "/@types%2Fnode") {
		t.Fatalf("a packument is asked for as /<name>: %s", got)
	}
}

func TestRegistryRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(f *fakeNPM)
		want string
	}{
		{"a 404", func(f *fakeNPM) { delete(f.pages, "/foo/1.0.1") }, "the registry answered 404 for foo 1.0.1"},
		{"a redirect", func(f *fakeNPM) {
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/foo/1.0.1" {
					http.Redirect(w, r, "https://elsewhere.test/", http.StatusMovedPermanently)
					return true
				}
				return false
			}
		}, "the registry answered 301 for foo 1.0.1"},
		{"a manifest of another version", func(f *fakeNPM) {
			f.pages["/foo/1.0.1"] = strings.Replace(f.pages["/foo/1.0.1"], `"1.0.1"`, `"1.0.2"`, 1)
		}, "the registry manifest is not that of foo 1.0.1"},
		{"a manifest that is not JSON", func(f *fakeNPM) { f.pages["/foo/1.0.1"] = "<html>" }, "the registry manifest of foo 1.0.1 is not JSON"},
		{"a manifest over the size limit", func(f *fakeNPM) {
			f.pages["/foo/1.0.1"] = `{"name":"foo","version":"1.0.1","pad":"` + strings.Repeat("x", maxManifestBytes) + `"}`
		}, "the registry manifest of foo 1.0.1 is too large"},
		{"a packument of another name", func(f *fakeNPM) {
			f.pages["/foo"] = strings.Replace(f.pages["/foo"], `"name":"foo"`, `"name":"bar"`, 1)
		}, "the registry packument is not that of foo"},
		{"a packument with no times", func(f *fakeNPM) {
			f.pages["/foo"] = `{"name":"foo","versions":{"1.0.1":{}}}`
		}, "the packument of foo has no publication times"},
	}
	for _, c := range cases {
		f := &fakeNPM{pages: honestPages()}
		c.edit(f)
		if got := run(t, f, 2*time.Second, 10*time.Second, 300); got != "FAIL: "+c.want {
			t.Errorf("%s: %q, want %q", c.name, got, "FAIL: "+c.want)
		}
	}
}

func TestRegistryBounds(t *testing.T) {
	stuck := func(_ http.ResponseWriter, _ *http.Request) bool {
		time.Sleep(1500 * time.Millisecond)
		return false
	}
	f := &fakeNPM{pages: honestPages(), hook: stuck}
	got, took := timedRun(t, f, 200*time.Millisecond, 5*time.Second, 300)
	if !strings.Contains(got, "could not be reached for foo") || !strings.Contains(got, "(TimeoutError)") || took > time.Second {
		t.Errorf("a request is bounded in all: %q after %s", got, took)
	}

	f = &fakeNPM{pages: honestPages(), hook: stuck}
	got, took = timedRun(t, f, 5*time.Second, 300*time.Millisecond, 300)
	if !strings.Contains(got, "did not finish in the time allowed") || took > time.Second {
		t.Errorf("the run is bounded in all: %q after %s", got, took)
	}

	f = &fakeNPM{pages: honestPages()}
	if got := run(t, f, time.Second, 5*time.Second, 1); got != "FAIL: too many new or changed packages to look up at the registry" {
		t.Errorf("the lookups share a budget of requests: %q", got)
	}
}

func TestRegistrySlowDripIsBounded(t *testing.T) {
	f := &fakeNPM{pages: honestPages(), hook: func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/foo/1.0.1" {
			return false
		}
		for i := 0; i < 20; i++ {
			_, _ = w.Write([]byte(" "))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
		return true
	}}
	got, took := timedRun(t, f, 300*time.Millisecond, 5*time.Second, 300)
	if !strings.Contains(got, "(TimeoutError)") || took > time.Second {
		t.Errorf("a server that drips is not waited for: %q after %s", got, took)
	}
}

// trackedBody records how often an answer is closed.
type trackedBody struct {
	io.Reader
	closes *int
}

func (b trackedBody) Close() error {
	*b.closes++
	return nil
}

// lookupThrough runs one lookup against an answer served by the transport, and returns the refusal
// it ended in ("" for none) and how often the answer was closed.
func lookupThrough(status int, body string, lookup func(*HTTPRegistry)) (string, int) {
	closes := 0
	r := newHTTPRegistry(time.Second, 5*time.Second, 300, 4)
	r.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: trackedBody{strings.NewReader(body), &closes}}, nil
	})}
	return core.Guard(func() { lookup(r) }), closes
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRegistryClosesEveryAnswer(t *testing.T) {
	packument := func(r *HTTPRegistry) { r.Packument("foo") }
	manifest := func(r *HTTPRegistry) { r.Get("foo", "1.0.1") }
	cases := []struct {
		label  string
		status int
		body   string
		lookup func(*HTTPRegistry)
		want   string
	}{
		{"a packument read", 200, honestPages()["/foo"], packument, ""},
		{"a manifest read", 200, honestPages()["/foo/1.0.1"], manifest, ""},
		{"an answer that is not a 200", 404, "gone", packument, "the registry answered 404 for foo None"},
		{"a packument refused while it arrives", 200, `{"name": "foo", "time": {},}`, packument, "the registry packument of foo is not JSON"},
		{"a packument of another name", 200, `{"name": "bar"}`, packument, "the registry packument is not that of foo"},
		{"a manifest over the limit", 200, `{"name":"foo","version":"1.0.1","pad":"` + strings.Repeat("x", maxManifestBytes) + `"}`, manifest,
			"the registry manifest of foo 1.0.1 is too large"},
		{"a manifest that is not JSON", 200, "<html>", manifest, "the registry manifest of foo 1.0.1 is not JSON"},
		{"a packument member over 8 MiB", 200, `{"name": "foo", "readme": "` + strings.Repeat("r", 8*1024*1024) + `"}`, packument,
			"a member of the registry packument of foo is too large"},
		{"a packument of members each under 8 MiB", 200, `{"name": "foo", "readme": "` + strings.Repeat("r", 8*1024*1024-100) + `", "time": {}}`, packument, ""},
	}
	for _, c := range cases {
		got, closes := lookupThrough(c.status, c.body, c.lookup)
		want := ""
		if c.want != "" {
			want = "FAIL: " + c.want
		}
		if got != want {
			t.Errorf("%s: %q, want %q", c.label, got, want)
		}
		if closes != 1 {
			t.Errorf("%s: the answer was closed %d times, want once", c.label, closes)
		}
	}
}
