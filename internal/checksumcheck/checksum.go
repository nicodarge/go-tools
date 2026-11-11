// Package checksumcheck decides whether a tool checksum changed only the way a version bump
// changes it, in a Dockerfile or a dotenv-style tool-pin file. The base and head files are
// compared line by line; the new digests are compared with what upstream publishes.
package checksumcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicodarge/go-tools/internal/core"
)

const (
	maxBytes            = 1 * 1024 * 1024
	maxDownloadBytes    = 8 * 1024 * 1024
	timeBudget          = 60 * time.Second
	perRequestTimeout   = 10 * time.Second
	allowedRedirectTail = ".githubusercontent.com"
	// The pin line a tool's `{ubuntu}` placeholder is read from: the one extra field a tag, asset or
	// checksums template can reference.
	ubuntuARG = "RUBY_UBUNTU_VERSION"
	usage     = "FAIL: usage: bump-checkers checksum [--urls FILE] <mapping file> <path> <base file> <head file>"
)

var allowedHosts = []string{"api.github.com", "github.com"}

const prefixSrc = `[A-Z0-9]+(?:_[A-Z0-9]+)*`

var (
	prefixRE  = regexp.MustCompile(`^` + prefixSrc + `\z`)
	envNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.env\z`)
	tokenRE   = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,99}\z`)
	sha256RE  = regexp.MustCompile(`^[0-9a-f]{64}\z`)
	repoRE    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+\z`)
	archRE    = regexp.MustCompile(`^[A-Z0-9]+\z`)
)

func reject(msg string) { core.Reject(msg) }

// say writes a line of the verdict and returns the error if it could not be written.
func say(out io.Writer, line string) error {
	_, err := fmt.Fprintln(out, line)
	return err
}

func q(x any) string { return core.Qn(x, 80) }

// Tool is one entry of the mapping.
type Tool struct {
	Repo      string
	Tag       string
	Assets    []asset // in the order the mapping lists them
	Checksums *string
}

type asset struct{ Arch, Name string }

func (t *Tool) assetName(arch string) (string, bool) {
	for _, a := range t.Assets {
		if a.Arch == arch {
			return a.Name, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// The mapping: scripts/checksums.py of the same repository, at the base tip.

// pyVal is a literal of the mapping file.
type pyVal struct {
	kind  string // str, bytes, int, float, complex, bool, None, Ellipsis, tuple, list, set, dict
	s     string
	items []pyVal
	pairs [][2]pyVal
}

func hashable(v pyVal) bool {
	switch v.kind {
	case "list", "dict", "set":
		return false
	case "tuple":
		for _, item := range v.items {
			if !hashable(item) {
				return false
			}
		}
	}
	return true
}

func sameKey(a, b pyVal) bool {
	if a.kind != b.kind || a.s != b.s || len(a.items) != len(b.items) {
		return false
	}
	for i := range a.items {
		if !sameKey(a.items[i], b.items[i]) {
			return false
		}
	}
	return true
}

// literalEval is ast.literal_eval: strings, numbers, containers of those, nothing else.
func literalEval(n *pyNode) (pyVal, bool) {
	switch n.kind {
	case nConst:
		names := map[constKind]string{cStr: "str", cBytes: "bytes", cInt: "int", cFloat: "float",
			cComplex: "complex", cBool: "bool", cNone: "None", cEllipsis: "Ellipsis"}
		return pyVal{kind: names[n.ckind], s: n.s}, true
	case nTuple, nList, nSet:
		kind := map[nodeKind]string{nTuple: "tuple", nList: "list", nSet: "set"}[n.kind]
		out := pyVal{kind: kind}
		for _, e := range n.elts {
			v, ok := literalEval(e)
			if !ok || (n.kind == nSet && !hashable(v)) {
				return pyVal{}, false
			}
			out.items = append(out.items, v)
		}
		return out, true
	case nDict:
		out := pyVal{kind: "dict"}
		for i, e := range n.elts {
			if n.keys[i] == nil {
				return pyVal{}, false
			}
			k, ok := literalEval(n.keys[i])
			if !ok || !hashable(k) {
				return pyVal{}, false
			}
			v, ok := literalEval(e)
			if !ok {
				return pyVal{}, false
			}
			replaced := false
			for j := range out.pairs {
				if sameKey(out.pairs[j][0], k) {
					out.pairs[j][1], replaced = v, true
				}
			}
			if !replaced {
				out.pairs = append(out.pairs, [2]pyVal{k, v})
			}
		}
		return out, true
	}
	return pyVal{}, false
}

func safeLiteral(n *pyNode, what string) pyVal {
	v, ok := literalEval(n)
	if !ok {
		reject(what + " is not a literal")
	}
	return v
}

var toolFields = []string{"prefix", "repo", "tag", "assets", "checksums"}

// ParseTools reads the TOOLS tuple of scripts/checksums.py as data; the file's code is never run.
func ParseTools(mappingText string) (tools map[string]*Tool, order []string) {
	var node *pyNode
	func() {
		defer func() {
			if r := recover(); r != nil {
				if s, ok := r.(pySyntax); ok {
					reject("scripts/checksums.py does not parse (" + s.msg + ")")
				}
				panic(r)
			}
		}()
		node = findTools(mappingText)
	}()
	if node == nil || (node.kind != nTuple && node.kind != nList) {
		reject("scripts/checksums.py has no top-level TOOLS tuple")
	}
	tools = map[string]*Tool{}
	for _, call := range node.elts {
		if call.kind != nCall || call.fn.kind != nName || call.fn.s != "Tool" {
			reject("scripts/checksums.py TOOLS holds something other than a Tool(...) call")
		}
		if len(call.args) > len(toolFields) {
			reject("a Tool(...) call in scripts/checksums.py has too many positional arguments")
		}
		values := map[string]pyVal{}
		for i, arg := range call.args {
			values[toolFields[i]] = safeLiteral(arg, "a Tool(...) argument")
		}
		for _, kw := range call.kws {
			_, taken := values[kw.name]
			if !core.InList(toolFields, kw.name) || taken {
				reject("a Tool(...) call in scripts/checksums.py carries an unexpected argument")
			}
			values[kw.name] = safeLiteral(kw.value, "the "+kw.name+" argument")
		}
		for _, required := range []string{"prefix", "repo", "tag", "assets"} {
			if _, ok := values[required]; !ok {
				reject("a Tool(...) call in scripts/checksums.py is missing a required field")
			}
		}

		prefixV := values["prefix"]
		if prefixV.kind != "str" || !prefixRE.MatchString(prefixV.s) {
			reject("a Tool(...) prefix in scripts/checksums.py is not an uppercase identifier")
		}
		prefix := prefixV.s
		repo := values["repo"]
		if repo.kind != "str" || !repoRE.MatchString(repo.s) {
			reject("the upstream repository for " + prefix + " in scripts/checksums.py is not owner/repo")
		}
		tag := values["tag"]
		if tag.kind != "str" {
			reject("the release tag template for " + prefix + " in scripts/checksums.py is not a string")
		}
		assetsV := values["assets"]
		ok := assetsV.kind == "dict" && len(assetsV.pairs) > 0
		var assets []asset
		if ok {
			for _, pair := range assetsV.pairs {
				if pair[0].kind != "str" || !archRE.MatchString(pair[0].s) || pair[1].kind != "str" {
					ok = false
					break
				}
				assets = append(assets, asset{pair[0].s, pair[1].s})
			}
		}
		if !ok {
			reject("the asset map for " + prefix + " in scripts/checksums.py is not a map of architecture to name")
		}
		var checksums *string
		if c, has := values["checksums"]; has && c.kind != "None" {
			if c.kind != "str" {
				reject("the checksums-file template for " + prefix + " in scripts/checksums.py is not a string")
			}
			s := c.s
			checksums = &s
		}
		if _, dup := tools[prefix]; dup {
			reject("scripts/checksums.py names " + prefix + " more than once")
		}
		tools[prefix] = &Tool{Repo: repo.s, Tag: tag.s, Assets: assets, Checksums: checksums}
		order = append(order, prefix)
	}
	if len(tools) == 0 {
		reject("scripts/checksums.py names no tool")
	}
	return tools, order
}

// ---------------------------------------------------------------------------
// The Dockerfile, or a dotenv-style tool-pin file: read whole, compared line by line, never through
// the diff.

// lineLead is what starts a pin line in this file: `ARG ` in a Dockerfile, nothing in a dotenv-style
// file (a name ending in `.env`).
func lineLead(path string) string {
	name := path[strings.LastIndex(path, "/")+1:]
	if name == "Dockerfile" {
		return "ARG "
	}
	if envNameRE.MatchString(name) {
		return ""
	}
	reject(q(path) + " is neither a Dockerfile nor a tool-pin env file")
	return ""
}

// pinLineREs are the VERSION and SHA256 line matchers for the files whose pin lines start with lead.
func pinLineREs(lead string) (version, sha *regexp.Regexp) {
	lead = regexp.QuoteMeta(lead)
	return regexp.MustCompile(`^` + lead + `(` + prefixSrc + `)_VERSION=(.*)\z`),
		regexp.MustCompile(`^` + lead + `(` + prefixSrc + `)_SHA256_([A-Z0-9]+)=([0-9a-fA-F]{64})\z`)
}

func pinFileLines(text, which string) []string {
	if strings.Contains(text, "\r") {
		reject("the " + which + " file carries a carriage return")
	}
	return strings.Split(text, "\n")
}

func findSingleArg(lines []string, name, lead string) (string, bool) {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(lead+name) + `=(.*)\z`)
	var found []string
	for _, line := range lines {
		if re.MatchString(line) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0][strings.Index(found[0], "=")+1:], true
}

func safeToken(value, what string) {
	if !tokenRE.MatchString(value) {
		reject(what + " is not a plain version token")
	}
}

type versionChange struct{ prefix, old, new string }
type shaChange struct{ prefix, arch, old, new string }

// changedPairs: each changed VERSION line is its own entry, never collapsed by prefix: a
// multi-stage Dockerfile can repeat `ARG <PREFIX>_VERSION=` once per stage, and keying by prefix
// alone would keep only the last such line seen, leaving an earlier one's new value unverified.
func changedPairs(path string, baseLines, headLines []string) ([]versionChange, []shaChange) {
	versionLineRE, shaLineRE := pinLineREs(lineLead(path))
	var versions []versionChange
	var shas []shaChange
	for i := range baseLines {
		old, nw := baseLines[i], headLines[i]
		if old == nw {
			continue
		}
		ov, nv := versionLineRE.FindStringSubmatch(old), versionLineRE.FindStringSubmatch(nw)
		if ov != nil && nv != nil && ov[1] == nv[1] && ov[2] != "" && nv[2] != "" && ov[2] != nv[2] {
			versions = append(versions, versionChange{ov[1], ov[2], nv[2]})
			continue
		}
		oldSha, newSha := shaLineRE.FindStringSubmatch(old), shaLineRE.FindStringSubmatch(nw)
		if oldSha != nil && newSha != nil && oldSha[1] == newSha[1] && oldSha[2] == newSha[2] &&
			!strings.EqualFold(oldSha[3], newSha[3]) {
			shas = append(shas, shaChange{oldSha[1], oldSha[2], strings.ToLower(oldSha[3]), strings.ToLower(newSha[3])})
			continue
		}
		reject(fmt.Sprintf("%s line %d changes in a way this does not recognise (%s to %s)", q(path), i+1, q(old), q(nw)))
	}
	return versions, shas
}

// rejectIfDowngrade: a downgrade is not an update. Versions that read as a dotted number with an
// optional `-prerelease` are compared as semver is (a release is newer than its own prerelease);
// any other token is compared by its digit and non-digit runs, as `sort -V` does.
func rejectIfDowngrade(prefix, oldVersion, newVersion string) {
	if !tokenRE.MatchString(newVersion) {
		return // refused as a bad token, whatever its order
	}
	if compareVersions(newVersion, oldVersion) < 0 {
		reject(fmt.Sprintf("%s_VERSION moves from %s to %s, which is older; a downgrade is not an update",
			prefix, q(oldVersion), q(newVersion)))
	}
}

// DigestFetcher says what upstream publishes for the architectures a diff changed.
type DigestFetcher func(tool *Tool, prefix, version string, ubuntu *string, arches []string) map[string]string

func check(tools map[string]*Tool, path, baseText, headText string, fetch DigestFetcher) {
	baseLines := pinFileLines(baseText, "base")
	headLines := pinFileLines(headText, "head")
	if len(baseLines) != len(headLines) {
		reject(q(path) + " gains or loses a line")
	}

	versions, shas := changedPairs(path, baseLines, headLines)
	if len(versions) != 1 {
		reject(fmt.Sprintf("%s changes %d version line(s); exactly one must change", q(path), len(versions)))
	}
	prefix, oldVersion, newVersion := versions[0].prefix, versions[0].old, versions[0].new
	rejectIfDowngrade(prefix, oldVersion, newVersion)
	if len(shas) == 0 {
		reject(q(path) + " changes a version with no checksum beside it")
	}

	var arches []string
	for _, s := range shas {
		if s.prefix != prefix {
			reject(fmt.Sprintf("%s changes a checksum of %s, but the version that changed is %s", q(path), s.prefix, prefix))
		}
		if core.InList(arches, s.arch) {
			reject(fmt.Sprintf("%s changes the %s checksum of %s more than once", q(path), s.arch, prefix))
		}
		if !sha256RE.MatchString(s.new) {
			reject(fmt.Sprintf("%s sets %s_SHA256_%s to something that is not a sha256 digest", q(path), prefix, s.arch))
		}
		arches = append(arches, s.arch)
	}

	tool := tools[prefix]
	if tool == nil {
		reject(prefix + " is not a tool scripts/checksums.py names")
	}
	for _, arch := range arches {
		if _, ok := tool.assetName(arch); !ok {
			reject(fmt.Sprintf("%s is not an architecture scripts/checksums.py names for %s", arch, prefix))
		}
	}

	safeToken(newVersion, "the new "+prefix+"_VERSION")
	var ubuntu *string
	needsUbuntu := strings.Contains(tool.Tag, "{ubuntu}")
	for _, a := range tool.Assets {
		needsUbuntu = needsUbuntu || strings.Contains(a.Name, "{ubuntu}")
	}
	if tool.Checksums != nil && strings.Contains(*tool.Checksums, "{ubuntu}") {
		needsUbuntu = true
	}
	if needsUbuntu {
		found, ok := findSingleArg(headLines, ubuntuARG, lineLead(path))
		if !ok {
			reject(fmt.Sprintf("%s needs %s, which %s does not set exactly once", prefix, ubuntuARG, q(path)))
		}
		safeToken(found, ubuntuARG)
		ubuntu = &found
	}

	digests := fetch(tool, prefix, newVersion, ubuntu, arches)
	for _, s := range shas {
		expected, ok := digests[s.arch]
		if !ok {
			reject(fmt.Sprintf("the upstream release of %s %s does not publish a digest for %s", prefix, newVersion, s.arch))
		}
		if s.new != expected {
			reject(fmt.Sprintf("%s sets %s_SHA256_%s to a digest that is not the one upstream publishes for %s",
				q(path), prefix, s.arch, newVersion))
		}
	}
}

// ---------------------------------------------------------------------------
// Upstream: the two GitHub hosts a release is read from, bounded.

// redirectAllowed: a redirect is followed only from one of our two hosts to GitHub's own asset
// host, over https: the fetched digest is the trust anchor, so a plaintext hop is never taken.
func redirectAllowed(origin string, target *url.URL) bool {
	o, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := strings.ToLower(o.Hostname())
	th := strings.ToLower(target.Hostname())
	return core.InList(allowedHosts, host) && strings.HasSuffix(th, allowedRedirectTail) && target.Scheme == "https"
}

type fixture struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// urlFixtures stands in for the network, as --manifests DIR does for the npm registry: for tests
// only, read by Main from a file of {url: {"status": int, "body": str}}, never from the environment.
// It must never be fed untrusted data.
type urlFixtures map[string]json.RawMessage

func urlopen(fixtures urlFixtures, rawURL string, timeout time.Duration, headers map[string]string) (int, []byte) {
	u, err := url.Parse(rawURL)
	hostname := ""
	if err == nil {
		hostname = strings.ToLower(u.Hostname())
	}
	if !core.InList(allowedHosts, hostname) {
		reject("refuses to fetch a URL whose host is " + q(hostname))
	}
	if fixtures != nil {
		raw, ok := fixtures[rawURL]
		if !ok {
			reject("no recorded answer for " + q(rawURL))
		}
		var f fixture
		if jsonErr := json.Unmarshal(raw, &f); jsonErr != nil {
			core.Crash("TypeError")
		}
		return f.Status, []byte(f.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		reject("could not be reached (ValueError)")
	}
	req.Header.Set("User-Agent", "bump-checkers")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Transport: httpTransport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !redirectAllowed(via[len(via)-1].URL.String(), req.URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		class := "URLError"
		if errors.Is(err, context.DeadlineExceeded) {
			class = "TimeoutError"
		}
		reject("could not be reached (" + class + ")")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			reject("the answer took too long")
		}
		reject("could not be reached (URLError)")
	}
	if len(data) > maxDownloadBytes {
		reject("the answer is larger than the limit")
	}
	return resp.StatusCode, data
}

// httpTransport is nil, the default transport, except in the tests.
var httpTransport http.RoundTripper

type budget struct {
	deadline   time.Time
	perRequest time.Duration
}

func newBudget() *budget { return &budget{time.Now().Add(timeBudget), perRequestTimeout} }

func (b *budget) timeout() time.Duration {
	remaining := time.Until(b.deadline)
	if remaining <= 0 {
		reject("the time allowed for upstream lookups ran out")
	}
	if remaining < b.perRequest {
		return remaining
	}
	return b.perRequest
}

// splitLines splits at every line boundary Unicode defines, which is more than "\n": the file
// being read must not hide a line from this check by a boundary another reader honours.
func splitLines(text string) []string {
	var lines []string
	runes := []rune(text)
	var cur []rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\u0085', '\u2028', '\u2029':
			lines = append(lines, string(cur))
			cur = nil
		case '\r':
			lines = append(lines, string(cur))
			cur = nil
			if i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		lines = append(lines, string(cur))
	}
	return lines
}

func fromChecksumFile(data []byte, names []asset, source string) map[string]string {
	if !utf8.Valid(data) {
		reject(source + " is not UTF-8")
	}
	found := map[string][]string{}
	for _, line := range splitLines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		digest := strings.ToLower(fields[0])
		name := strings.TrimPrefix(strings.TrimLeft(fields[1], "*"), "./")
		for _, a := range names {
			if name == a.Name {
				found[a.Arch] = append(found[a.Arch], digest)
			}
		}
	}
	result := map[string]string{}
	for _, a := range names {
		matches := found[a.Arch]
		if len(matches) != 1 || !sha256RE.MatchString(matches[0]) {
			reject(fmt.Sprintf("%s must hold exactly one sha256 line for %s, found %d", source, q(a.Name), len(matches)))
		}
		result[a.Arch] = matches[0]
	}
	return result
}

func fromRelease(data []byte, names []asset, source string) map[string]string {
	var payload any
	if !utf8.Valid(data) {
		reject(source + " is not JSON")
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		reject(source + " is not JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		reject(source + " is not JSON")
	}
	var assets []map[string]any
	ok := false
	if m, isMap := payload.(map[string]any); isMap {
		if list, isList := m["assets"].([]any); isList {
			ok = true
			for _, a := range list {
				am, isObj := a.(map[string]any)
				if !isObj {
					ok = false
					break
				}
				assets = append(assets, am)
			}
		}
	}
	if !ok {
		reject(source + " is not a release")
	}
	result := map[string]string{}
	for _, a := range names {
		var found []any
		for _, asset := range assets {
			if name, isStr := asset["name"].(string); isStr && name == a.Name {
				found = append(found, asset["digest"])
			}
		}
		var digest string
		if len(found) == 1 {
			digest, _ = found[0].(string)
		}
		if len(found) != 1 || !strings.HasPrefix(digest, "sha256:") {
			reject(fmt.Sprintf("%s must list exactly one asset %s with a sha256 digest", source, q(a.Name)))
		}
		digest = strings.ToLower(strings.TrimPrefix(digest, "sha256:"))
		if !sha256RE.MatchString(digest) {
			reject(fmt.Sprintf("%s lists a malformed digest for %s", source, q(a.Name)))
		}
		result[a.Arch] = digest
	}
	return result
}

// pyQuote is urllib.parse.quote with its default safe characters.
func pyQuote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte("_.-~/", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// pyFormat fills a template of literal text and plain `{name}` fields, `{{` and `}}` being literal
// braces. A name that is not a field, and any other replacement-field syntax, fails closed.
func pyFormat(template string, fields map[string]string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(template); i++ {
		c := template[i]
		switch {
		case c == '{' && i+1 < len(template) && template[i+1] == '{':
			b.WriteByte('{')
			i++
		case c == '}' && i+1 < len(template) && template[i+1] == '}':
			b.WriteByte('}')
			i++
		case c == '}':
			core.Crash("ValueError")
		case c == '{':
			end := strings.IndexByte(template[i:], '}')
			if end < 0 {
				core.Crash("ValueError")
			}
			name := template[i+1 : i+end]
			if strings.ContainsAny(name, "!:.[{") {
				core.Crash("ValueError")
			}
			value, ok := fields[name]
			if !ok {
				return "", false
			}
			b.WriteString(value)
			i += end
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

func fetchDigests(fixtures urlFixtures, tool *Tool, prefix, version string, ubuntu *string, arches []string, b *budget) map[string]string {
	fields := map[string]string{"version": version}
	if ubuntu != nil {
		fields["ubuntu"] = *ubuntu
	}
	tag, ok := pyFormat(tool.Tag, fields)
	var names []asset
	for _, arch := range arches {
		if !ok {
			break
		}
		template, _ := tool.assetName(arch)
		var name string
		if name, ok = pyFormat(template, fields); ok {
			names = append(names, asset{arch, name})
		}
	}
	if !ok {
		reject("the asset templates for " + prefix + " need a field this diff does not provide")
	}

	if tool.Checksums != nil && *tool.Checksums != "" {
		fileName, ok := pyFormat(*tool.Checksums, fields)
		if !ok {
			reject("the checksums-file template for " + prefix + " needs a field this diff does not provide")
		}
		u := "https://github.com/" + tool.Repo + "/releases/download/" + pyQuote(tag) + "/" + pyQuote(fileName)
		status, data := urlopen(fixtures, u, b.timeout(), nil)
		if status != 200 {
			reject(fmt.Sprintf("%s answered %d", u, status))
		}
		return fromChecksumFile(data, names, u)
	}

	u := "https://api.github.com/repos/" + tool.Repo + "/releases/tags/" + pyQuote(tag)
	status, data := urlopen(fixtures, u, b.timeout(), map[string]string{"Accept": "application/vnd.github+json"})
	if status != 200 {
		reject(fmt.Sprintf("%s answered %d", u, status))
	}
	return fromRelease(data, names, u)
}

// ---------------------------------------------------------------------------

func openError(err error) {
	switch {
	case os.IsNotExist(err):
		core.Crash("FileNotFoundError")
	case os.IsPermission(err):
		core.Crash("PermissionError")
	}
	if strings.Contains(err.Error(), "is a directory") {
		core.Crash("IsADirectoryError")
	}
	core.Crash("OSError")
}

// readText reads a file named on the command line, which is trusted to name what is meant.
func readText(path, which string) string {
	f, err := core.OpenFile(path)
	if err != nil {
		openError(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		openError(err)
	}
	if int64(len(data)) > maxBytes {
		reject("the " + which + " file is larger than the limit")
	}
	if !utf8.Valid(data) {
		reject("the " + which + " file is not UTF-8")
	}
	return string(data)
}

// equivDigests, set only by the equivalence build, stands in for what upstream publishes.
var equivDigests map[string]string

// Main runs the checksum check on args (after the subcommand) and writes its verdict to out: PASS
// and 0, or `FAIL: <reason>` and 1.
func Main(args []string, out io.Writer) int {
	var urlsFile *string
	if len(args) >= 2 && args[0] == "--urls" {
		urlsFile, args = &args[1], args[2:]
	}
	if len(args) != 4 {
		_ = say(out, usage)
		return 1
	}
	mappingFile, path, baseFile, headFile := args[0], args[1], args[2], args[3]
	line := core.Guard(func() {
		var fixtures urlFixtures
		if urlsFile != nil {
			text := readText(*urlsFile, "urls")
			if err := json.Unmarshal([]byte(text), &fixtures); err != nil {
				core.Crash("JSONDecodeError")
			}
			if fixtures == nil {
				fixtures = urlFixtures{}
			}
		}
		tools, _ := ParseTools(readText(mappingFile, "mapping"))
		baseText := readText(baseFile, "base")
		headText := readText(headFile, "head")
		b := newBudget()
		check(tools, path, baseText, headText, func(tool *Tool, prefix, version string, ubuntu *string, arches []string) map[string]string {
			if equivDigests != nil {
				return equivDigests
			}
			return fetchDigests(fixtures, tool, prefix, version, ubuntu, arches, b)
		})
	})
	if line != "" {
		_ = say(out, line)
		return 1
	}
	if say(out, "PASS") != nil {
		return 1 // a PASS nobody could read is not one
	}
	return 0
}
