package apptoken

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	keysOnce sync.Once
	keyA     *rsa.PrivateKey
	keyB     *rsa.PrivateKey
)

func testKeys(t *testing.T) (a, b *rsa.PrivateKey) {
	t.Helper()
	keysOnce.Do(func() {
		var err error
		if keyA, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if keyB, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return keyA, keyB
}

func pkcs1PEM(key *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func pkcs8PEM(t *testing.T, key crypto.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func ecKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pkcs8PEM(t, key)
}

// armor wraps body in the PEM lines of the given type.
func armor(typ, body string) string {
	return "-----" + "BEGIN " + typ + "-----\n" + body + "\n-----" + "END " + typ + "-----\n"
}

// pemBody is the base64 text between the PEM lines: what must never be printed.
func pemBody(p string) string {
	var body []string
	for _, line := range strings.Split(p, "\n") {
		if line != "" && !strings.HasPrefix(line, "-----") {
			body = append(body, line)
		}
	}
	return strings.Join(body, "")
}

// verifyJWT checks the RS256 signature of token against pub and returns its claims.
func verifyJWT(token string, pub *rsa.PublicKey) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, errors.New("not three parts")
	}
	enc := base64.RawURLEncoding
	headerRaw, err := enc.DecodeString(parts[0])
	if err != nil {
		return jwtClaims{}, err
	}
	var header map[string]string
	if err = json.Unmarshal(headerRaw, &header); err != nil {
		return jwtClaims{}, err
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		return jwtClaims{}, errors.New("bad header")
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return jwtClaims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return jwtClaims{}, err
	}
	claimsRaw, err := enc.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, err
	}
	var claims jwtClaims
	return claims, json.Unmarshal(claimsRaw, &claims)
}

type recorded struct {
	method, path, auth, accept, version, agent string
	body                                       string
	jwtOK                                      bool
	claims                                     jwtClaims
}

type reply struct {
	status int
	body   string
}

// fakeGitHub answers by "METHOD path"; an unlisted route is a 404. A request authenticated with a
// JWT that the App's public key does not verify gets GitHub's 401.
type fakeGitHub struct {
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]reply
	calls  []recorded
}

func newFakeGitHub(t *testing.T, pub *rsa.PublicKey, routes map[string]reply) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{routes: routes}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := recorded{
			method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"),
			accept: r.Header.Get("Accept"), version: r.Header.Get("X-GitHub-Api-Version"),
			agent: r.Header.Get("User-Agent"), body: string(body),
		}
		bearer := strings.TrimPrefix(rec.auth, "Bearer ")
		if strings.Count(bearer, ".") == 2 {
			claims, err := verifyJWT(bearer, pub)
			rec.jwtOK, rec.claims = err == nil, claims
			if err != nil {
				f.record(rec)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"message":"A JSON web token could not be decoded"}`)
				return
			}
		}
		f.record(rec)
		rep, ok := f.routes[r.Method+" "+r.URL.Path]
		if !ok {
			rep = reply{http.StatusNotFound, `{"message":"Not Found"}`}
		}
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) record(r recorded) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r)
}

func (f *fakeGitHub) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.method+" "+c.path)
	}
	return out
}

func (f *fakeGitHub) call(t *testing.T, key string) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.method+" "+c.path == key {
			return c
		}
	}
	t.Fatalf("no %s call; calls: %v", key, f.calls)
	return recorded{}
}

const (
	issued   = "fake-installation-0123456789abcdefghijklmnopqr"
	fixedNow = 1_800_000_000
)

func tokenReply() reply {
	return reply{http.StatusCreated, `{"token":"` + issued + `","expires_at":"2030-01-01T00:00:00Z"}`}
}

func happyRoutes() map[string]reply {
	return map[string]reply{
		"GET /repos/org/repo/installation":           {http.StatusOK, `{"id":4242}`},
		"GET /orgs/org/installation":                 {http.StatusOK, `{"id":4242}`},
		"POST /app/installations/4242/access_tokens": tokenReply(),
		"GET /app": {http.StatusOK, `{"slug":"my-app","id":1}`},
	}
}

type result struct {
	code           int
	stdout, stderr string
	output         string
}

type tripwireWriter struct {
	buf     bytes.Buffer
	onFirst func()
	once    sync.Once
}

func (w *tripwireWriter) Write(p []byte) (int, error) {
	w.once.Do(func() {
		if w.onFirst != nil {
			w.onFirst()
		}
	})
	return w.buf.Write(p)
}

// run executes Main against the fake server with the given extra environment. GITHUB_OUTPUT is a
// fresh file unless the environment names one.
func run(t *testing.T, f *fakeGitHub, env map[string]string, args ...string) result {
	t.Helper()
	outFile := filepath.Join(t.TempDir(), "github_output")
	if err := os.WriteFile(outFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"GITHUB_OUTPUT": outFile}
	if f != nil {
		vars["GITHUB_API_URL"] = f.srv.URL
	}
	for k, v := range env {
		vars[k] = v
	}
	var stdout, stderr bytes.Buffer
	code := Main(args, Env{
		Getenv: func(k string) string { return vars[k] },
		Stdout: &stdout,
		Stderr: &stderr,
		Now:    func() time.Time { return time.Unix(fixedNow, 0) },
	})
	data, err := os.ReadFile(vars["GITHUB_OUTPUT"])
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return result{code: code, stdout: stdout.String(), stderr: stderr.String(), output: string(data)}
}

func baseEnv(t *testing.T) map[string]string {
	t.Helper()
	a, _ := testKeys(t)
	return map[string]string{
		"APP_TOKEN_CLIENT_ID":   "Iv23liTESTCLIENT",
		"APP_TOKEN_PRIVATE_KEY": pkcs1PEM(a),
		"GITHUB_REPOSITORY":     "org/repo",
	}
}
