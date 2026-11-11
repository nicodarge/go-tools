package lockfilecheck

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nicodarge/go-tools/internal/core"
)

const (
	maxBytes         = 64 * 1024 * 1024
	maxManifestBytes = 8 * 1024 * 1024
	maxDownloadBytes = 64 * 1024 * 1024
)

// registryURL is a variable so the tests can point the registry at a server of their own.
var registryURL = "https://registry.npmjs.org"

type pair struct {
	name    string
	version string // unused for a packument
	isPack  bool
}

// Registry looks a name@version up, or the packument of a name; every failure is a Reject.
type Registry interface {
	Prefetch(pairs []pair)
	Get(name, version string) *core.Value
	Packument(name string) *core.Packument
}

func versionText(p pair) string {
	if p.isPack {
		return "None"
	}
	return core.Q(p.version)
}

func parseManifest(r io.Reader, name, version string) (*core.Value, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestBytes {
		core.Reject("the registry manifest of " + core.Q(name) + " " + core.Q(version) + " is too large")
	}
	manifest, ok := core.ParseJSON(data, false)
	if !ok {
		core.Reject("the registry manifest of " + core.Q(name) + " " + core.Q(version) + " is not JSON")
	}
	if !core.IsMap(manifest) || !core.IsStr(manifest.M.Get("name")) || manifest.M.Get("name").S != name ||
		!core.IsStr(manifest.M.Get("version")) || manifest.M.Get("version").S != version {
		core.Reject("the registry manifest is not that of " + core.Q(name) + " " + core.Q(version))
	}
	return manifest, nil
}

func checkPackument(p *core.Packument, name string) *core.Packument {
	if p.Name == nil || !core.IsStr(p.Name) || p.Name.S != name {
		core.Reject("the registry packument is not that of " + core.Q(name))
	}
	return p
}

// ---------------------------------------------------------------------------

// DirRegistry reads manifests from `<name with / as %2F>@<version>.json` and packuments from
// `<name with / as %2F>.packument.json`. Only the tests use it.
type DirRegistry struct{ path string }

// NewDirRegistry reads recorded answers from path.
func NewDirRegistry(path string) *DirRegistry { return &DirRegistry{path} }

// Prefetch does nothing: every answer is a file.
func (d *DirRegistry) Prefetch([]pair) {}

// Get reads the recorded manifest of name@version.
func (d *DirRegistry) Get(name, version string) *core.Value {
	f, err := core.OpenIn(d.path, strings.ReplaceAll(name, "/", "%2F")+"@"+version+".json")
	if err != nil {
		core.Reject("no recorded manifest of " + core.Q(name) + " " + core.Q(version))
	}
	defer func() { _ = f.Close() }()
	manifest, readErr := parseManifest(f, name, version)
	if readErr != nil {
		core.Reject("no recorded manifest of " + core.Q(name) + " " + core.Q(version))
	}
	return manifest
}

// Packument reads the recorded packument of name.
func (d *DirRegistry) Packument(name string) *core.Packument {
	f, err := core.OpenIn(d.path, strings.ReplaceAll(name, "/", "%2F")+".packument.json")
	if err != nil {
		core.Reject("no recorded packument of " + core.Q(name))
	}
	defer func() { _ = f.Close() }()
	p, readErr := core.ParsePackument(f, name, maxDownloadBytes)
	if readErr != nil {
		core.Reject("no recorded packument of " + core.Q(name))
	}
	return checkPackument(p, name)
}

// ---------------------------------------------------------------------------

type result struct {
	manifest *core.Value
	pack     *core.Packument
	err      any // a Reject or an internal error to raise again
}

// HTTPRegistry asks registry.npmjs.org. Each request is bounded in all, however slowly the server
// answers, and so is the run.
type HTTPRegistry struct {
	baseURL    string
	perRequest time.Duration
	limit      int
	workers    int
	deadline   time.Time
	client     *http.Client

	mu    sync.Mutex
	cache map[pair]result
}

// NewHTTPRegistry asks registry.npmjs.org within the default bounds.
func NewHTTPRegistry() *HTTPRegistry {
	return newHTTPRegistry(5*time.Second, 90*time.Second, 300, 4)
}

func newHTTPRegistry(perRequest, budget time.Duration, limit, workers int) *HTTPRegistry {
	return &HTTPRegistry{
		baseURL:    registryURL,
		perRequest: perRequest,
		limit:      limit,
		workers:    workers,
		deadline:   time.Now().Add(budget),
		client: &http.Client{
			// A redirect is an answer from somewhere else, and is refused.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		cache: map[pair]result{},
	}
}

func (h *HTTPRegistry) url(p pair) string {
	u := h.baseURL + "/" + strings.ReplaceAll(p.name, "/", "%2F")
	if !p.isPack {
		u += "/" + p.version
	}
	return u
}

func unreachable(p pair, err error) {
	class := "URLError"
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		class = "TimeoutError"
	}
	core.Reject("the registry could not be reached for " + core.Q(p.name) + " " + versionText(p) + " (" + class + ")")
}

func (h *HTTPRegistry) load(p pair) result {
	remaining := time.Until(h.deadline)
	if remaining <= 0 {
		core.Reject("the time allowed for registry lookups ran out")
	}
	timeout := h.perRequest
	if remaining < timeout {
		timeout = remaining
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url(p), nil)
	if err != nil {
		unreachable(p, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bump-checkers")
	resp, err := h.client.Do(req)
	if err != nil {
		unreachable(p, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		core.Reject("the registry answered " + core.Q(resp.StatusCode) + " for " + core.Q(p.name) + " " + versionText(p))
	}
	if p.isPack {
		pk, readErr := core.ParsePackument(resp.Body, p.name, maxDownloadBytes)
		if readErr != nil {
			unreachable(p, readErr)
		}
		return result{pack: checkPackument(pk, p.name)}
	}
	manifest, readErr := parseManifest(resp.Body, p.name, p.version)
	if readErr != nil {
		unreachable(p, readErr)
	}
	return result{manifest: manifest}
}

func (h *HTTPRegistry) attempt(p pair) {
	var res result
	func() {
		defer func() {
			if r := recover(); r != nil {
				res = result{err: r}
			}
		}()
		res = h.load(p)
	}()
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, done := h.cache[p]; !done {
		h.cache[p] = res
	}
}

// Prefetch looks the pairs up concurrently, once each, within the request and time budgets.
func (h *HTTPRegistry) Prefetch(pairs []pair) {
	h.mu.Lock()
	var todo []pair
	seen := map[pair]bool{}
	for _, p := range pairs {
		if _, cached := h.cache[p]; !cached && !seen[p] {
			seen[p] = true
			todo = append(todo, p)
		}
	}
	cached := len(h.cache)
	h.mu.Unlock()
	sort.Slice(todo, func(i, j int) bool {
		if todo[i].name != todo[j].name {
			return todo[i].name < todo[j].name
		}
		return todo[i].version < todo[j].version
	})
	if cached+len(todo) > h.limit {
		core.Reject("too many new or changed packages to look up at the registry")
	}
	sem := make(chan struct{}, h.workers)
	var wg sync.WaitGroup
	for _, p := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			h.attempt(p)
		}()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	wait := time.Until(h.deadline)
	if wait < 0 {
		wait = 0
	}
	select {
	case <-finished:
	case <-time.After(wait):
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range todo {
		if _, done := h.cache[p]; !done {
			h.cache[p] = result{err: core.Rejection{Msg: "a registry lookup did not finish in the time allowed"}}
		}
	}
}

func (h *HTTPRegistry) get(p pair) result {
	h.mu.Lock()
	res, ok := h.cache[p]
	h.mu.Unlock()
	if !ok {
		h.attempt(p)
		h.mu.Lock()
		res = h.cache[p]
		h.mu.Unlock()
	}
	if res.err != nil {
		panic(res.err)
	}
	return res
}

// Get is the manifest of name@version.
func (h *HTTPRegistry) Get(name, version string) *core.Value {
	return h.get(pair{name: name, version: version}).manifest
}

// Packument is the packument of name.
func (h *HTTPRegistry) Packument(name string) *core.Packument {
	return h.get(pair{name: name, isPack: true}).pack
}
