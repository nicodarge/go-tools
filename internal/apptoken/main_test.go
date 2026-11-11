package apptoken

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func outputLines(s string) []string {
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func TestMintCurrentRepository(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	res := run(t, f, baseEnv(t), "mint", "--permission", "contents=write")
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}
	wantOut := "token=" + issued + "\ninstallation-id=4242\napp-slug=my-app\n"
	if res.output != wantOut {
		t.Errorf("GITHUB_OUTPUT = %q, want %q", res.output, wantOut)
	}
	if res.stdout != "::add-mask::"+issued+"\n" {
		t.Errorf("stdout = %q", res.stdout)
	}
	if got := f.paths(); strings.Join(got, ",") != "GET /repos/org/repo/installation,POST /app/installations/4242/access_tokens" {
		t.Errorf("calls = %v", got)
	}
	post := f.call(t, "POST /app/installations/4242/access_tokens")
	if post.body != `{"permissions":{"contents":"write"},"repositories":["repo"]}` {
		t.Errorf("scoping body = %s", post.body)
	}
	for _, c := range f.calls {
		if !c.jwtOK || c.claims.Iss != "Iv23liTESTCLIENT" || c.claims.Iat != fixedNow-60 {
			t.Errorf("%s %s: JWT not verified or wrong claims: %+v", c.method, c.path, c)
		}
		if c.accept != "application/vnd.github+json" || c.version != apiVersion || c.agent == "" {
			t.Errorf("%s %s: headers %q %q %q", c.method, c.path, c.accept, c.version, c.agent)
		}
	}
}

func TestMintScopes(t *testing.T) {
	a, _ := testKeys(t)
	routes := happyRoutes()
	routes["GET /users/solo/installation"] = reply{http.StatusOK, `{"id":4242,"app_slug":"my-app"}`}
	routes["GET /repos/org/other/installation"] = reply{http.StatusOK, `{"id":4242,"app_slug":"my-app"}`}
	for label, tc := range map[string]struct {
		args  []string
		env   map[string]string
		calls string
		body  string
	}{
		"an organization, every repository": {
			args:  []string{"mint", "--owner", "org"},
			calls: "GET /orgs/org/installation,POST /app/installations/4242/access_tokens",
			body:  `{}`,
		},
		"a user, after the organization lookup 404s": {
			args:  []string{"mint", "--owner", "solo"},
			calls: "GET /orgs/solo/installation,GET /users/solo/installation,POST /app/installations/4242/access_tokens",
			body:  `{}`,
		},
		"an owner and repositories, looked up on the first": {
			args:  []string{"mint", "--owner", "org", "--repositories", "other,repo", "--permission", "pull-requests=write", "--permission", "contents=read"},
			calls: "GET /repos/org/other/installation,POST /app/installations/4242/access_tokens",
			body:  `{"permissions":{"contents":"read","pull_requests":"write"},"repositories":["other","repo"]}`,
		},
		"repositories only, the current owner": {
			args:  []string{"mint", "--repositories", "repo\nother"},
			calls: "GET /repos/org/repo/installation,POST /app/installations/4242/access_tokens",
			body:  `{"repositories":["repo","other"]}`,
		},
		"repositories in OWNER/NAME form, any case": {
			args:  []string{"mint", "--repositories", "org/repo, ORG/other\n,"},
			calls: "GET /repos/org/repo/installation,POST /app/installations/4242/access_tokens",
			body:  `{"repositories":["repo","other"]}`,
		},
		"empty items in the list": {
			args:  []string{"mint", "--repositories", "repo, ,other"},
			calls: "GET /repos/org/repo/installation,POST /app/installations/4242/access_tokens",
			body:  `{"repositories":["repo","other"]}`,
		},
		"the owner variable beats the repository's owner": {
			args:  []string{"mint", "--repositories", "other"},
			env:   map[string]string{"GITHUB_REPOSITORY_OWNER": "org", "GITHUB_REPOSITORY": "elsewhere/repo"},
			calls: "GET /repos/org/other/installation,POST /app/installations/4242/access_tokens",
			body:  `{"repositories":["other"]}`,
		},
	} {
		f := newFakeGitHub(t, &a.PublicKey, routes)
		env := baseEnv(t)
		for k, v := range tc.env {
			env[k] = v
		}
		res := run(t, f, env, tc.args...)
		if res.code != 0 {
			t.Errorf("%s: exit %d, stderr %q", label, res.code, res.stderr)
			continue
		}
		if got := strings.Join(f.paths(), ","); got != tc.calls {
			t.Errorf("%s: calls %s, want %s", label, got, tc.calls)
		}
		if got := f.call(t, "POST /app/installations/4242/access_tokens").body; got != tc.body {
			t.Errorf("%s: body %s, want %s", label, got, tc.body)
		}
	}
}

func TestMintKeySources(t *testing.T) {
	a, b := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	dir := t.TempDir()
	file := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(file, []byte(pkcs8PEM(t, a)), 0o600); err != nil {
		t.Fatal(err)
	}
	env := baseEnv(t)
	delete(env, "APP_TOKEN_PRIVATE_KEY")
	if res := run(t, f, env, "mint", "--private-key-file", file); res.code != 0 {
		t.Errorf("a PKCS#8 key file: exit %d, stderr %q", res.code, res.stderr)
	}
	env["APP_TOKEN_PRIVATE_KEY"] = pkcs8PEM(t, a)
	if res := run(t, f, env, "mint"); res.code != 0 {
		t.Errorf("a PKCS#8 key in the environment: exit %d, stderr %q", res.code, res.stderr)
	}
	if res := run(t, f, env, "mint", "--private-key-file", file); res.code != 1 || !strings.Contains(res.stderr, "exclusive") {
		t.Errorf("both sources: exit %d, stderr %q", res.code, res.stderr)
	}
	delete(env, "APP_TOKEN_PRIVATE_KEY")
	if res := run(t, f, env, "mint"); res.code != 1 || !strings.Contains(res.stderr, "no private key") {
		t.Errorf("no source: exit %d, stderr %q", res.code, res.stderr)
	}
	if res := run(t, f, env, "mint", "--private-key-file", filepath.Join(dir, "absent.pem")); res.code != 1 || res.output != "" {
		t.Errorf("a missing key file: exit %d, output %q", res.code, res.output)
	}
	// A key of another App: GitHub's 401 for a JWT it cannot verify.
	env["APP_TOKEN_PRIVATE_KEY"] = pkcs1PEM(b)
	res := run(t, f, env, "mint")
	if res.code != 1 || !strings.Contains(res.stderr, "HTTP 401") || res.output != "" || res.stdout != "" {
		t.Errorf("a key GitHub rejects: exit %d, stderr %q, stdout %q, output %q", res.code, res.stderr, res.stdout, res.output)
	}
}

func TestMintFailuresPrintNothingSecret(t *testing.T) {
	a, _ := testKeys(t)
	notFound := happyRoutes()
	delete(notFound, "GET /repos/org/repo/installation")
	forbidden := happyRoutes()
	forbidden["POST /app/installations/4242/access_tokens"] = reply{http.StatusForbidden, `{"message":"Resource not accessible by integration"}`}
	noToken := happyRoutes()
	noToken["POST /app/installations/4242/access_tokens"] = reply{http.StatusCreated, `{"token":""}`}
	spaced := happyRoutes()
	spaced["POST /app/installations/4242/access_tokens"] = reply{http.StatusCreated, `{"token":"fake-a\nb=c"}`}
	notJSON := happyRoutes()
	notJSON["POST /app/installations/4242/access_tokens"] = reply{http.StatusCreated, `<html>` + issued + `</html>`}
	noID := happyRoutes()
	noID["GET /repos/org/repo/installation"] = reply{http.StatusOK, `{}`}
	wrongStatus := happyRoutes()
	wrongStatus["POST /app/installations/4242/access_tokens"] = reply{http.StatusOK, `{"token":"` + issued + `"}`}

	for label, tc := range map[string]struct {
		routes map[string]reply
		want   string
	}{
		"no installation (404)":      {notFound, "HTTP 404: Not Found"},
		"a 403 on the token request": {forbidden, "HTTP 403: Resource not accessible by integration"},
		"an empty token":             {noToken, "no token"},
		"a token with a newline":     {spaced, "white space or control"},
		"a token answer not JSON":    {notJSON, "not the expected JSON"},
		"an installation with no id": {noID, "no id"},
		"a 200 where 201 is due":     {wrongStatus, "HTTP 200"},
	} {
		f := newFakeGitHub(t, &a.PublicKey, tc.routes)
		env := baseEnv(t)
		res := run(t, f, env, "mint")
		if res.code != 1 {
			t.Errorf("%s: exit %d", label, res.code)
		}
		if !strings.Contains(res.stderr, tc.want) {
			t.Errorf("%s: stderr %q lacks %q", label, res.stderr, tc.want)
		}
		if res.output != "" || res.stdout != "" {
			t.Errorf("%s: wrote outputs %q / stdout %q", label, res.output, res.stdout)
		}
		all := res.stdout + res.stderr + res.output
		for _, secret := range []string{pemBody(env["APP_TOKEN_PRIVATE_KEY"])[:40], "eyJ", issued, "BEGIN"} {
			if strings.Contains(all, secret) {
				t.Errorf("%s: output holds %q: %q", label, secret, all)
			}
		}
	}
}

func TestMintMalformedKeyReachesNoServer(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	env := baseEnv(t)
	lines := strings.Split(env["APP_TOKEN_PRIVATE_KEY"], "\n")
	for label, key := range map[string]string{
		"garbage":       "not a pem",
		"corrupt PKCS1": armor("RSA PRIVATE KEY", lines[1]),
		"an EC key":     ecKeyPEM(t),
	} {
		env["APP_TOKEN_PRIVATE_KEY"] = key
		res := run(t, f, env, "mint")
		if res.code != 1 || res.output != "" || res.stdout != "" {
			t.Errorf("%s: exit %d, stdout %q, output %q", label, res.code, res.stdout, res.output)
		}
		if strings.Contains(res.stderr, lines[1]) {
			t.Errorf("%s: stderr quotes the key", label)
		}
	}
	if got := f.paths(); len(got) != 0 {
		t.Errorf("a malformed key still reached the server: %v", got)
	}
}

func TestMintSlugFailureIsNotFatal(t *testing.T) {
	a, _ := testKeys(t)
	for label, installation := range map[string]string{
		"no app_slug":           `{"id":4242}`,
		"a slug with a newline": `{"id":4242,"app_slug":"bad\nslug"}`,
		"a slug with a slash":   `{"id":4242,"app_slug":"a/b"}`,
		"an empty app_slug":     `{"id":4242,"app_slug":""}`,
	} {
		routes := happyRoutes()
		routes["GET /repos/org/repo/installation"] = reply{http.StatusOK, installation}
		res := run(t, newFakeGitHub(t, &a.PublicKey, routes), baseEnv(t), "mint")
		if res.code != 0 || res.output != "token="+issued+"\ninstallation-id=4242\n" {
			t.Errorf("%s: exit %d, output %q", label, res.code, res.output)
		}
		if !strings.Contains(res.stderr, "::warning::") {
			t.Errorf("%s: stderr = %q", label, res.stderr)
		}
	}
}

func TestMintReadsNoAppEndpoint(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	if res := run(t, f, baseEnv(t), "mint"); res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}
	for _, p := range f.paths() {
		if p == "GET /app" {
			t.Errorf("the App was fetched: %v", f.paths())
		}
	}
}

func TestMintRetries(t *testing.T) {
	a, _ := testKeys(t)
	const post = "POST /app/installations/4242/access_tokens"
	boom := reply{http.StatusBadGateway, `{"message":"Bad Gateway"}`}
	for label, tc := range map[string]struct {
		seqs   map[string][]reply
		code   int
		posts  int
		looks  int
		sleeps []time.Duration
	}{
		"502, 502, 201": {
			seqs: map[string][]reply{post: {boom, boom, tokenReply()}},
			code: 0, posts: 3, looks: 3, sleeps: []time.Duration{time.Second, 2 * time.Second},
		},
		"a 5xx on the lookup": {
			seqs: map[string][]reply{"GET /repos/org/repo/installation": {boom, {http.StatusOK, `{"id":4242,"app_slug":"my-app"}`}}},
			code: 0, posts: 1, looks: 2, sleeps: []time.Duration{time.Second},
		},
		"502 four times": {
			seqs: map[string][]reply{post: {boom}},
			code: 1, posts: 4, looks: 4, sleeps: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
		},
		"a 403 is final": {
			seqs: map[string][]reply{post: {{http.StatusForbidden, `{"message":"no"}`}}},
			code: 1, posts: 1, looks: 1,
		},
		"a 404 on the lookup is final": {
			seqs: map[string][]reply{"GET /repos/org/repo/installation": {{http.StatusNotFound, `{"message":"Not Found"}`}}},
			code: 1, posts: 0, looks: 1,
		},
		"a 422 is final": {
			seqs: map[string][]reply{post: {{http.StatusUnprocessableEntity, `{"message":"bad"}`}}},
			code: 1, posts: 1, looks: 1,
		},
	} {
		f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
		f.seqs = tc.seqs
		res := run(t, f, baseEnv(t), "mint")
		if res.code != tc.code {
			t.Errorf("%s: exit %d, stderr %q", label, res.code, res.stderr)
		}
		posts, looks := 0, 0
		for _, p := range f.paths() {
			switch p {
			case post:
				posts++
			case "GET /repos/org/repo/installation":
				looks++
			}
		}
		if posts != tc.posts || looks != tc.looks {
			t.Errorf("%s: %d lookups and %d token requests, want %d and %d", label, looks, posts, tc.looks, tc.posts)
		}
		if !slices.Equal(res.sleeps, tc.sleeps) {
			t.Errorf("%s: delays %v, want %v", label, res.sleeps, tc.sleeps)
		}
		if tc.code == 0 && res.output != "token="+issued+"\ninstallation-id=4242\napp-slug=my-app\n" {
			t.Errorf("%s: output %q", label, res.output)
		}
		if tc.code == 1 && (res.output != "" || res.stdout != "") {
			t.Errorf("%s: wrote outputs %q / %q", label, res.output, res.stdout)
		}
	}
}

func TestMintRetriesATransportError(t *testing.T) {
	a, _ := testKeys(t)
	good := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	var hits int
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		good.srv.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(flaky.Close)
	res := run(t, nil, withAPI(baseEnv(t), flaky.URL), "mint")
	if res.code != 0 || !slices.Equal(res.sleeps, []time.Duration{time.Second}) {
		t.Errorf("exit %d, delays %v, stderr %q", res.code, res.sleeps, res.stderr)
	}
}

func TestMintStopsRetryingWhenTheContextEnds(t *testing.T) {
	a, _ := testKeys(t)
	routes := happyRoutes()
	routes["POST /app/installations/4242/access_tokens"] = reply{http.StatusBadGateway, `{"message":"Bad Gateway"}`}
	f := newFakeGitHub(t, &a.PublicKey, routes)
	env := baseEnv(t)
	var stderr strings.Builder
	var sleeps int
	code := Main([]string{"mint"}, Env{
		Getenv: func(k string) string {
			if k == "GITHUB_API_URL" {
				return f.srv.URL
			}
			return env[k]
		},
		Stdout: io.Discard,
		Stderr: &stderr,
		Sleep: func(_ context.Context, _ time.Duration) error {
			sleeps++
			return context.Canceled
		},
	})
	if code != 1 || sleeps != 1 || !strings.Contains(stderr.String(), "HTTP 502") {
		t.Errorf("exit %d, %d sleeps, stderr %q", code, sleeps, stderr.String())
	}
}

func TestMintRevokesWhenThePublishFails(t *testing.T) {
	a, _ := testKeys(t)
	for label, tc := range map[string]struct {
		del  reply
		warn bool
	}{
		"revoked":           {reply{http.StatusNoContent, ``}, false},
		"revocation failed": {reply{http.StatusInternalServerError, `{"message":"boom"}`}, true},
	} {
		routes := happyRoutes()
		routes["DELETE /installation/token"] = tc.del
		f := newFakeGitHub(t, &a.PublicKey, routes)
		env := baseEnv(t)
		env["GITHUB_OUTPUT"] = filepath.Join(t.TempDir(), "absent-dir", "out")
		res := run(t, f, env, "mint")
		if res.code != 1 || !strings.Contains(res.stderr, "writing GITHUB_OUTPUT") {
			t.Errorf("%s: exit %d, stderr %q", label, res.code, res.stderr)
		}
		if got := f.call(t, "DELETE /installation/token").auth; got != "Bearer "+issued {
			t.Errorf("%s: the token was not revoked: %q", label, got)
		}
		if warned := strings.Contains(res.stderr, "::warning::app-token: token revocation failed"); warned != tc.warn {
			t.Errorf("%s: warning = %v, stderr %q", label, warned, res.stderr)
		}
		if strings.Contains(res.stdout+res.stderr, "token="+issued) {
			t.Errorf("%s: the token output was printed", label)
		}
	}
}

func TestFilesBehindSymbolicLinks(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, map[string]reply{
		"GET /repos/org/repo/installation":           {http.StatusOK, `{"id":4242,"app_slug":"my-app"}`},
		"POST /app/installations/4242/access_tokens": tokenReply(),
		"DELETE /installation/token":                 {http.StatusNoContent, ``},
	})
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "mount"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, "data", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("key.pem", pkcs1PEM(a))
	write("token", issued+"\n")
	write("out", "")
	// A relative link that leaves its directory, as a mounted secret does.
	for _, name := range []string{"key.pem", "token", "out"} {
		if err := os.Symlink(filepath.Join("..", "data", name), filepath.Join(root, "mount", name)); err != nil {
			t.Fatal(err)
		}
	}
	env := baseEnv(t)
	delete(env, "APP_TOKEN_PRIVATE_KEY")
	env["GITHUB_OUTPUT"] = filepath.Join(root, "mount", "out")
	res := run(t, f, env, "mint", "--private-key-file", filepath.Join(root, "mount", "key.pem"))
	if res.code != 0 {
		t.Fatalf("mint: exit %d, stderr %q", res.code, res.stderr)
	}
	got, err := os.ReadFile(filepath.Clean(filepath.Join(root, "data", "out")))
	if err != nil || string(got) != "token="+issued+"\ninstallation-id=4242\napp-slug=my-app\n" {
		t.Errorf("GITHUB_OUTPUT behind a link: %q, %v", got, err)
	}
	if res := run(t, f, nil, "revoke", "--token-file", filepath.Join(root, "mount", "token")); res.code != 0 || res.stderr != "" {
		t.Errorf("revoke: exit %d, stderr %q", res.code, res.stderr)
	}
	if got := f.call(t, "DELETE /installation/token").auth; got != "Bearer "+issued {
		t.Errorf("authorization %q", got)
	}
}

func TestLoopbackURLsPassTheSchemeCheck(t *testing.T) {
	for url, ok := range map[string]bool{
		"http://[::1]:8080":       true,
		"http://localhost:8080":   true,
		"http://LOCALHOST":        true,
		"http://127.0.0.1:1":      true,
		"http://127.1.2.3":        true,
		"http://[::2]:8080":       false,
		"http://0.0.0.0:8080":     false,
		"http://[::ffff:8.8.8.8]": false,
		"https://example.com":     true,
	} {
		_, err := newClient(Env{Getenv: func(string) string { return url }})
		if (err == nil) != ok {
			t.Errorf("%s: error %v, accepted want %v", url, err, ok)
		}
	}
}

func TestMintKeyWithEscapedNewlines(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	escaped := strings.ReplaceAll(strings.TrimSpace(pkcs1PEM(a)), "\n", `\n`)
	env := baseEnv(t)
	env["APP_TOKEN_PRIVATE_KEY"] = escaped
	if res := run(t, f, env, "mint"); res.code != 0 {
		t.Errorf("an escaped key in the environment: exit %d, stderr %q", res.code, res.stderr)
	}
	file := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(file, []byte(escaped), 0o600); err != nil {
		t.Fatal(err)
	}
	delete(env, "APP_TOKEN_PRIVATE_KEY")
	if res := run(t, f, env, "mint", "--private-key-file", file); res.code != 0 {
		t.Errorf("an escaped key file: exit %d, stderr %q", res.code, res.stderr)
	}
}

func TestLoopbackHostsMayUseHTTP(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for label, tc := range map[string]struct {
		network, addr, host string
	}{
		"localhost":     {"tcp4", "127.0.0.1:0", "localhost"},
		"IPv4 loopback": {"tcp4", "127.0.0.1:0", "127.0.0.1"},
		"IPv6 loopback": {"tcp6", "[::1]:0", "[::1]"},
	} {
		l, err := net.Listen(tc.network, tc.addr)
		if err != nil {
			t.Logf("%s: cannot listen: %v", label, err)
			continue
		}
		srv := httptest.NewUnstartedServer(handler)
		srv.Listener = l
		srv.Start()
		_, port, _ := net.SplitHostPort(l.Addr().String())
		res := run(t, nil, withAPI(map[string]string{"APP_TOKEN": issued}, "http://"+tc.host+":"+port), "revoke")
		srv.Close()
		if res.code != 0 || res.stderr != "" {
			t.Errorf("%s: exit %d, stderr %q", label, res.code, res.stderr)
		}
	}
}

func TestFilesAreBounded(t *testing.T) {
	big := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", maxKeyFile+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := run(t, nil, nil, "revoke", "--token-file", big); res.code != 1 || !strings.Contains(res.stderr, "too large") {
		t.Errorf("exit %d, stderr %q", res.code, res.stderr)
	}
}

func TestMintWithoutGithubOutput(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	env := baseEnv(t)
	env["GITHUB_OUTPUT"] = ""
	res := run(t, f, env, "mint")
	lines := outputLines(res.stdout)
	if res.code != 0 || lines[0] != "::add-mask::"+issued || lines[1] != "token="+issued {
		t.Errorf("exit %d, stdout %q", res.code, res.stdout)
	}
}

// The mask command is the first thing written anywhere: before the token reaches $GITHUB_OUTPUT
// and before it reaches the standard output.
func TestMaskComesBeforeAnythingElse(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	env := baseEnv(t)
	outFile := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(outFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var sizeAtFirstWrite int64 = -1
	stdout := &tripwireWriter{}
	stdout.onFirst = func() {
		if st, err := os.Stat(outFile); err == nil {
			sizeAtFirstWrite = st.Size()
		}
	}
	var stderr strings.Builder
	code := Main([]string{"mint"}, Env{
		Getenv: func(k string) string {
			switch k {
			case "GITHUB_OUTPUT":
				return outFile
			case "GITHUB_API_URL":
				return f.srv.URL
			}
			return env[k]
		},
		Stdout: stdout,
		Stderr: &stderr,
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if sizeAtFirstWrite != 0 {
		t.Errorf("$GITHUB_OUTPUT held %d bytes when the first line was printed", sizeAtFirstWrite)
	}
	if first := outputLines(stdout.buf.String())[0]; first != "::add-mask::"+issued {
		t.Errorf("first line printed = %q", first)
	}
	if strings.Contains(stdout.buf.String(), "token=") {
		t.Errorf("the token output leaked to stdout: %q", stdout.buf.String())
	}
}

func TestMintDoesNotFollowRedirects(t *testing.T) {
	a, _ := testKeys(t)
	other := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.srv.URL+r.URL.Path)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	res := run(t, nil, withAPI(baseEnv(t), redirector.URL), "mint")
	if res.code != 1 || !strings.Contains(res.stderr, "HTTP 307") {
		t.Errorf("exit %d, stderr %q", res.code, res.stderr)
	}
	if got := other.paths(); len(got) != 0 {
		t.Errorf("the redirect was followed: %v", got)
	}
}

func withAPI(env map[string]string, url string) map[string]string {
	env["GITHUB_API_URL"] = url
	return env
}

func TestMintUsageAndInputErrors(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	for label, tc := range map[string]struct {
		args []string
		env  map[string]string
		code int
		want string
	}{
		"no command":                         {nil, nil, 2, "usage:"},
		"an unknown command":                 {[]string{"mine"}, nil, 2, "unknown command"},
		"an unknown flag":                    {[]string{"mint", "--token", "x"}, nil, 2, "usage:"},
		"a positional argument":              {[]string{"mint", "extra"}, nil, 2, "usage:"},
		"no client id":                       {[]string{"mint"}, map[string]string{"APP_TOKEN_CLIENT_ID": ""}, 1, "no client ID"},
		"client id and app id":               {[]string{"mint", "--client-id", "a", "--app-id", "1"}, nil, 1, "exclusive"},
		"a client id with a slash":           {[]string{"mint", "--client-id", "a/b"}, nil, 1, "unexpected character"},
		"no owner at all":                    {[]string{"mint"}, map[string]string{"GITHUB_REPOSITORY": ""}, 1, "no --owner"},
		"a path in the repository":           {[]string{"mint", "--repositories", "../x"}, nil, 1, "not a repository name"},
		"another owner's repository":         {[]string{"mint", "--repositories", "other/repo"}, nil, 1, "does not match"},
		"a dot owner in OWNER/NAME":          {[]string{"mint", "--repositories", "../repo"}, nil, 1, "not a repository name"},
		"two slashes":                        {[]string{"mint", "--repositories", "org/repo/x"}, nil, 1, "not a repository name"},
		"a space in a name":                  {[]string{"mint", "--repositories", "a b"}, nil, 1, "not a repository name"},
		"a dot repository":                   {[]string{"mint", "--repositories", ".."}, nil, 1, "not a repository name"},
		"a slash in the owner":               {[]string{"mint", "--owner", "a/b"}, nil, 1, "not a GitHub account name"},
		"a dot owner":                        {[]string{"mint", "--owner", ".."}, nil, 1, "not a GitHub account name"},
		"a single dot owner":                 {[]string{"mint", "--owner", "."}, nil, 1, "not a GitHub account name"},
		"a bad owner variable":               {[]string{"mint"}, map[string]string{"GITHUB_REPOSITORY_OWNER": "../x"}, 1, "GITHUB_REPOSITORY_OWNER"},
		"a dot owner variable":               {[]string{"mint"}, map[string]string{"GITHUB_REPOSITORY_OWNER": ".."}, 1, "GITHUB_REPOSITORY_OWNER"},
		"a bad repository variable":          {[]string{"mint"}, map[string]string{"GITHUB_REPOSITORY": "org/re po"}, 1, "GITHUB_REPOSITORY is not"},
		"a repository variable, no slash":    {[]string{"mint"}, map[string]string{"GITHUB_REPOSITORY": "repo"}, 1, "GITHUB_REPOSITORY is not"},
		"a repository variable, a dot owner": {[]string{"mint"}, map[string]string{"GITHUB_REPOSITORY": "../repo"}, 1, "GITHUB_REPOSITORY is not"},
		"a repository variable, a dot name":  {[]string{"mint"}, map[string]string{"GITHUB_REPOSITORY": "org/.."}, 1, "GITHUB_REPOSITORY is not"},
		"a permission, no level":             {[]string{"mint", "--permission", "contents"}, nil, 1, "want NAME="},
		"a permission, bad level":            {[]string{"mint", "--permission", "contents=owner"}, nil, 1, "want NAME="},
		"a permission, bad name":             {[]string{"mint", "--permission", "1contents=write"}, nil, 1, "want NAME="},
		"a permission, a space":              {[]string{"mint", "--permission", "con tents=write"}, nil, 1, "want NAME="},
		"two levels for one name":            {[]string{"mint", "--permission", "contents=read", "--permission", "contents=write"}, nil, 1, "twice"},
		"a file API URL":                     {[]string{"mint"}, map[string]string{"GITHUB_API_URL": "file:///etc"}, 1, "not an http(s) URL"},
		"a plain http API URL":               {[]string{"mint"}, map[string]string{"GITHUB_API_URL": "http://api.example.com"}, 1, "must be https"},
		"a plain http IP API URL":            {[]string{"mint"}, map[string]string{"GITHUB_API_URL": "http://10.0.0.1:8080"}, 1, "must be https"},
		"a localhost lookalike":              {[]string{"mint"}, map[string]string{"GITHUB_API_URL": "http://localhost.example.com"}, 1, "must be https"},
	} {
		res := run(t, f, withEnv(baseEnv(t), tc.env), tc.args...)
		if res.code != tc.code || !strings.Contains(res.stderr, tc.want) {
			t.Errorf("%s: exit %d, stderr %q (want %d, %q)", label, res.code, res.stderr, tc.code, tc.want)
		}
		if res.output != "" || res.stdout != "" {
			t.Errorf("%s: wrote outputs %q / %q", label, res.output, res.stdout)
		}
	}
	if got := f.paths(); len(got) != 0 {
		t.Errorf("an invalid request reached the server: %v", got)
	}
}

func withEnv(base, extra map[string]string) map[string]string {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func TestAppIDIsAnIssuer(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, happyRoutes())
	env := baseEnv(t)
	delete(env, "APP_TOKEN_CLIENT_ID")
	if res := run(t, f, env, "mint", "--app-id", "123456"); res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}
	if iss := f.call(t, "GET /repos/org/repo/installation").claims.Iss; iss != "123456" {
		t.Errorf("iss = %q", iss)
	}
}

func TestRevoke(t *testing.T) {
	a, _ := testKeys(t)
	for label, tc := range map[string]struct {
		reply  reply
		stdout string
		stderr string
	}{
		"revoked":              {reply{http.StatusNoContent, ``}, "", ""},
		"already gone (401)":   {reply{http.StatusUnauthorized, `{"message":"Bad credentials"}`}, "already revoked", ""},
		"a server error (500)": {reply{http.StatusInternalServerError, `{"message":"boom"}`}, "", "::warning::app-token: token revocation failed: DELETE /installation/token: HTTP 500: boom"},
		"a forbidden (403)":    {reply{http.StatusForbidden, `{"message":"no"}`}, "", "::warning::app-token: token revocation failed: DELETE /installation/token: HTTP 403: no"},
		"a control character":  {reply{http.StatusBadRequest, `{"message":"a\u001b[31mb"}`}, "", "HTTP 400: a [31mb\n"},
		"a multi-line message": {reply{http.StatusBadRequest, `{"message":"a\n::error::forged"}`}, "", "token revocation failed: DELETE /installation/token: HTTP 400: a ::error::forged\n"},
	} {
		f := newFakeGitHub(t, &a.PublicKey, map[string]reply{"DELETE /installation/token": tc.reply})
		res := run(t, f, map[string]string{"APP_TOKEN": issued}, "revoke")
		if res.code != 0 || !strings.Contains(res.stdout, tc.stdout) || !strings.Contains(res.stderr, tc.stderr) || (tc.stderr == "" && res.stderr != "") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", label, res.code, res.stdout, res.stderr)
		}
		if strings.ContainsRune(res.stderr, '\x1b') {
			t.Errorf("%s: a control character reached stderr: %q", label, res.stderr)
		}
		if len(res.sleeps) != 0 {
			t.Errorf("%s: the revocation was retried: %v", label, res.sleeps)
		}
		call := f.call(t, "DELETE /installation/token")
		if call.auth != "Bearer "+issued {
			t.Errorf("%s: authorization %q", label, call.auth)
		}
		if strings.Contains(res.stdout+res.stderr, issued) {
			t.Errorf("%s: the token was printed", label)
		}
	}
}

func TestRevokeNetworkErrorIsAWarning(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	res := run(t, nil, withAPI(map[string]string{"APP_TOKEN": issued}, url), "revoke")
	if res.code != 0 || !strings.Contains(res.stderr, "::warning::app-token: token revocation failed: DELETE /installation/token: request failed") {
		t.Errorf("exit %d, stderr %q", res.code, res.stderr)
	}
	if strings.Contains(res.stdout+res.stderr, issued) {
		t.Errorf("the token was printed")
	}
}

func TestRevokeInputs(t *testing.T) {
	a, _ := testKeys(t)
	f := newFakeGitHub(t, &a.PublicKey, map[string]reply{"DELETE /installation/token": {http.StatusNoContent, ``}})
	if res := run(t, f, nil, "revoke"); res.code != 0 || !strings.Contains(res.stdout, "no token to revoke") {
		t.Errorf("no token: exit %d, stdout %q", res.code, res.stdout)
	}
	if got := f.paths(); len(got) != 0 {
		t.Errorf("no token still sent a request: %v", got)
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(issued+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := run(t, f, nil, "revoke", "--token-file", file); res.code != 0 {
		t.Errorf("a token file: exit %d, stderr %q", res.code, res.stderr)
	}
	if got := f.call(t, "DELETE /installation/token").auth; got != "Bearer "+issued {
		t.Errorf("authorization %q", got)
	}
	if res := run(t, f, map[string]string{"APP_TOKEN": "x"}, "revoke", "--token-file", file); res.code != 1 {
		t.Errorf("both sources: exit %d", res.code)
	}
	if res := run(t, f, map[string]string{"APP_TOKEN": "a b"}, "revoke"); res.code != 1 || strings.Contains(res.stderr, "a b") {
		t.Errorf("a token with a space: exit %d, stderr %q", res.code, res.stderr)
	}
	if res := run(t, f, nil, "revoke", "--token-file", filepath.Join(t.TempDir(), "absent")); res.code != 1 {
		t.Errorf("a missing token file: exit %d", res.code)
	}
	if res := run(t, f, nil, "revoke", "extra"); res.code != 2 {
		t.Errorf("a positional argument: exit %d", res.code)
	}
}

func TestParsePermissions(t *testing.T) {
	got, err := parsePermissions([]string{"contents=write", "pull-requests=read", "contents=write"})
	if err != nil || len(got) != 2 || got["contents"] != "write" || got["pull_requests"] != "read" {
		t.Errorf("got %v, %v", got, err)
	}
	if got, err := parsePermissions([]string{"organization_projects=read", "Pull-Requests2=read"}); err != nil || got["organization_projects"] != "read" || got["pull_requests2"] != "read" {
		t.Errorf("digits: got %v, %v", got, err)
	}
	if _, err := parsePermissions([]string{"2fa=read"}); err == nil {
		t.Errorf("a name starting with a digit was accepted")
	}
	for _, name := range []string{"pull_requests", "Pull-Requests", "PULL_REQUESTS", "pull-requests"} {
		got, err := parsePermissions([]string{name + "=write"})
		if err != nil || len(got) != 1 || got["pull_requests"] != "write" {
			t.Errorf("%s: got %v, %v", name, got, err)
		}
	}
	if got, err := parsePermissions([]string{"pull-requests=read", "Pull_Requests=read"}); err != nil || len(got) != 1 {
		t.Errorf("one name spelled twice: got %v, %v", got, err)
	}
	if _, err := parsePermissions([]string{"pull-requests=read", "pull_requests=write"}); err == nil {
		t.Errorf("one name with two levels was accepted")
	}
	if got, err := parsePermissions(nil); got != nil || err != nil {
		t.Errorf("none: %v, %v", got, err)
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\n::error::x\r\ty"); got != "a ::error::x  y" {
		t.Errorf("got %q", got)
	}
}

func TestErrorMessageTruncatesOnRunes(t *testing.T) {
	raw := []byte(`{"message":"` + strings.Repeat("é", maxMessage+50) + `"}`)
	got := errorMessage(raw)
	if got != strings.Repeat("é", maxMessage) || !utf8.ValidString(got) {
		t.Errorf("got %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}
