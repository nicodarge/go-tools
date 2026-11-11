package lockfilecheck

import (
	"strings"

	"github.com/nicodarge/go-tools/internal/core"
)

func canonicalTrue(v *value) bool {
	return isStr(v) && v.Style == core.StylePlain && v.S == "true"
}

func depVersionEnd(text string, pos, depth int) int {
	loc := pnpmVersionRE.FindStringIndex(text[pos:])
	if loc == nil {
		reject(q(text) + " is not a semver version")
	}
	pos += loc[1]
	for pos < len(text) && text[pos] == '(' {
		if depth >= 8 {
			reject("peer suffixes nested too deeply")
		}
		named := pnpmNameAtRE.FindStringIndex(text[pos+1:])
		if named == nil {
			reject(q(text) + " has a peer suffix that is not name@version")
		}
		pos = depVersionEnd(text, pos+1+named[1], depth+1)
		if pos >= len(text) || text[pos] != ')' {
			reject(q(text) + " has an unterminated peer suffix")
		}
		pos++
	}
	return pos
}

func depVersion(v *value, what string) {
	if !isStr(v) || depVersionEnd(v.S, 0, 0) != len(v.S) {
		reject(what + " " + q(v) + " is not <semver> with peer suffixes")
	}
}

func beforeParen(s string) string {
	if i := strings.IndexByte(s, '('); i >= 0 {
		return s[:i]
	}
	return s
}

func pnpmPackageEntry(name, version string, entry, baseEntry *value, what string, todo *[]todoItem) {
	needDict(entry, what)
	core.NeedOnlyKeys(entry.M, []string{"resolution", "engines", "cpu", "os", "libc", "deprecated",
		"peerDependencies", "peerDependenciesMeta", "hasBin"}, what)
	resolutionV := entry.M.Get("resolution")
	resolution := needDict(resolutionV, what+" resolution")
	if !isNone(baseEntry) {
		needDict(baseEntry, "the base "+what)
	}
	if resolution.Len() != 1 || !resolution.Has("integrity") || !isIntegrity(resolution.Get("integrity")) {
		reject(what + " resolution is not exactly an integrity hash")
	}
	if !isNone(baseEntry) && !canonEq(baseEntry.M.Get("resolution"), resolutionV) {
		reject(what + " changes its integrity under an unchanged version")
	}
	plainMetadata(entry, what)
	if entry.M.Has("peerDependencies") {
		dependencyMap(entry.M.Get("peerDependencies"), what+" peerDependencies")
	}
	if entry.M.Has("peerDependenciesMeta") {
		meta := needDict(entry.M.Get("peerDependenciesMeta"), what+" peerDependenciesMeta")
		for _, dep := range meta.Keys {
			m := meta.Vals[dep]
			if !isName(str(dep)) || !isMap(m) || m.M.Len() != 1 || !m.M.Has("optional") ||
				!canonicalTrue(m.M.Get("optional")) {
				reject(what + " peerDependenciesMeta " + q(dep) + " is not optional: true")
			}
		}
	}
	if entry.M.Has("hasBin") && !canonicalTrue(entry.M.Get("hasBin")) {
		reject(what + " hasBin is not true")
	}
	if !isNone(baseEntry) {
		for _, key := range []string{"peerDependencies", "peerDependenciesMeta", "hasBin"} {
			if !sameKey(entry, baseEntry, key) {
				reject(what + " changes " + key + " under an unchanged version")
			}
		}
	}
	*todo = append(*todo, todoItem{name, version, func(manifest *value) {
		pnpmPackageAgainst(name, entry, manifest, what)
	}})
}

func pnpmPackageAgainst(name string, entry, manifest *value, what string) {
	manifestIntegrity(manifest, entry.M.Get("resolution").M.Get("integrity"), what)
	manifestNoInstall(manifest, what)
	if !strMapEq(core.ItemsOf(entry.M.Get("peerDependencies")), manifestMap(manifest, "peerDependencies", what)) {
		reject(what + " peerDependencies are not the ones the registry publishes")
	}
	published := orEmpty(mget(manifest, "peerDependenciesMeta"))
	if !isMap(published) {
		reject(what + ": the manifest peerDependenciesMeta is not a map of mappings")
	}
	for _, k := range published.M.Keys {
		if !isMap(published.M.Vals[k]) {
			reject(what + ": the manifest peerDependenciesMeta is not a map of mappings")
		}
	}
	// pnpm records only the peers that are optional.
	optional := map[string]bool{}
	for _, k := range published.M.Keys {
		if isTrue(published.M.Vals[k].M.Get("optional")) {
			optional[k] = true
		}
	}
	have := core.ItemsOf(entry.M.Get("peerDependenciesMeta"))
	same := have.Len() == len(optional)
	if same {
		for _, k := range have.Keys {
			if !optional[k] {
				same = false
			}
		}
	}
	if !same {
		reject(what + " peerDependenciesMeta is not the one the registry publishes")
	}
	if entry.M.Has("hasBin") != manifestHasBin(manifest, name, what) {
		reject(what + " hasBin is not what the registry publishes")
	}
}

// claimSet is an insertion-ordered map of (name, version) to the ranges that ask for it.
type claimSet struct {
	keys   [][2]string
	ranges map[[2]string][]*value
}

func newClaimSet() *claimSet { return &claimSet{ranges: map[[2]string][]*value{}} }

func (c *claimSet) add(name, version string, ranges ...*value) {
	k := [2]string{name, version}
	if _, ok := c.ranges[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.ranges[k] = append(c.ranges[k], ranges...)
}

func (c *claimSet) has(name, version string) bool {
	_, ok := c.ranges[[2]string{name, version}]
	return ok
}

type edge struct{ dep, version string }

func pnpmSnapshotEntry(name, version string, entry, baseEntry *value, siblings []*value, what string,
	todo *[]todoItem, claims *claimSet) {
	needDict(entry, what)
	core.NeedOnlyKeys(entry.M, []string{"dependencies", "optionalDependencies", "transitivePeerDependencies", "optional"}, what)
	depKinds := []string{"dependencies", "optionalDependencies"}
	for _, key := range depKinds {
		if entry.M.Has(key) {
			m := needDict(entry.M.Get(key), what+" "+key)
			for _, dep := range m.Keys {
				if !isName(str(dep)) {
					reject(what + " " + key + " names " + q(dep))
				}
				depVersion(m.Vals[dep], what+" "+key+" "+q(dep))
			}
		}
	}
	if entry.M.Has("transitivePeerDependencies") {
		stringList(entry.M.Get("transitivePeerDependencies"), what+" transitivePeerDependencies")
		for _, dep := range entry.M.Get("transitivePeerDependencies").L {
			if !isName(dep) {
				reject(what + " transitivePeerDependencies names " + q(dep))
			}
		}
	}
	if entry.M.Has("optional") && !canonicalTrue(entry.M.Get("optional")) {
		reject(what + " optional is not true")
	}
	if !isNone(baseEntry) {
		needDict(baseEntry, "the base "+what)
		for _, key := range depKinds {
			a, b := core.ItemsOf(entry.M.Get(key)), core.ItemsOf(baseEntry.M.Get(key))
			same := a.Len() == b.Len()
			if same {
				for _, k := range a.Keys {
					if !b.Has(k) {
						same = false
					}
				}
			}
			if !same {
				reject(what + " gains or loses a name in " + key)
			}
		}
	}
	// Against the same key in the base, or for a new key against the variants of the same package
	// version: a dependency never resolves below the lowest version the base had for it.
	before := siblings
	if !isNone(baseEntry) {
		before = []*value{baseEntry}
	}
	for _, key := range depKinds {
		deps := core.ItemsOf(entry.M.Get(key))
		for _, dep := range deps.Keys {
			resolved := deps.Vals[dep].S
			var earlier []string
			for _, other := range before {
				for _, kind := range depKinds {
					m := core.ItemsOf(mget(other, kind))
					if m.Has(dep) {
						earlier = append(earlier, beforeParen(m.Vals[dep].S))
					}
				}
			}
			if len(earlier) > 0 {
				lowest := earlier[0]
				for _, e := range earlier[1:] {
					if cmpVersion(parseVersion(e), parseVersion(lowest)) < 0 {
						lowest = e
					}
				}
				// min() parses every candidate, first to last.
				for _, e := range earlier {
					parseVersion(e)
				}
				notLower(beforeParen(resolved), lowest, what+" "+key+" "+q(dep))
			}
		}
	}
	// What is new here, for rule 6: every dependency of a new key, and any that moved in an old one.
	var edges []edge
	for _, key := range depKinds {
		deps := core.ItemsOf(entry.M.Get(key))
		for _, dep := range deps.Keys {
			resolved := beforeParen(deps.Vals[dep].S)
			var held []string
			if !isNone(baseEntry) {
				for _, kind := range depKinds {
					if o := core.ItemsOf(baseEntry.M.Get(kind)).Get(dep); o != nil {
						held = append(held, beforeParen(o.S))
					}
				}
			}
			if !core.InList(held, resolved) {
				edges = append(edges, edge{dep, resolved})
			}
		}
	}
	*todo = append(*todo, todoItem{name, version, func(manifest *value) {
		pnpmSnapshotAgainst(entry, manifest, what, edges, claims)
	}})
}

// pnpmSnapshotAgainst: what the snapshot resolves must be what the manifest asks for, in the
// version it asks for. Every dependency version is checked, not only the ones that moved: an
// existing package at a version outside the range is the downgrade this exists to stop.
// `overrides` and `packageExtensions` can legitimately resolve outside a manifest's range, and a
// bump that depends on one is refused with the rest.
func pnpmSnapshotAgainst(entry, manifest *value, what string, edges []edge, claims *claimSet) {
	ranges := map[string][]*value{}
	for _, kind := range []string{"dependencies", "optionalDependencies", "peerDependencies"} {
		m := manifestMap(manifest, kind, what)
		for _, dep := range m.Keys {
			ranges[dep] = append(ranges[dep], m.Vals[dep])
		}
	}
	for _, key := range []string{"dependencies", "optionalDependencies"} {
		deps := core.ItemsOf(entry.M.Get(key))
		for _, dep := range deps.Keys {
			rs, ok := ranges[dep]
			if !ok {
				reject(what + " " + key + " name " + q(dep) + ", which the registry manifest does not declare")
			}
			for _, spec := range rs {
				mustSatisfy(beforeParen(deps.Vals[dep].S), spec, what+" "+key+" "+q(dep))
			}
		}
	}
	// A peer-suffixed key is a variant of its package: it may resolve more or fewer peers, so the
	// manifest, not a sibling key, says what it must hold.
	optional := manifestMap(manifest, "optionalDependencies", what)
	peers := manifestMap(manifest, "peerDependencies", what)
	have := core.ItemsOf(entry.M.Get("dependencies"))
	mdeps := manifestMap(manifest, "dependencies", what)
	for _, dep := range mdeps.Keys {
		if !optional.Has(dep) && !peers.Has(dep) && !have.Has(dep) {
			reject(what + " leaves out " + q(dep) + ", a dependency the registry manifest declares")
		}
	}
	for _, e := range edges {
		claims.add(e.dep, e.version, ranges[e.dep]...)
	}
}

func lowestEntry(entries []*value, what string) *value {
	text := func(o *value) string {
		return beforeParen(core.PyStr(needDict(o, "the base "+what).Get("version")))
	}
	best := entries[0]
	bestKey := parseVersion(text(best))
	for _, e := range entries[1:] {
		k := parseVersion(text(e))
		if cmpVersion(k, bestKey) < 0 {
			best, bestKey = e, k
		}
	}
	return best
}

// pnpmDependency: `baseEntry` is the same entry in the base, `others` the entries of the same name
// in the base's other sections, or other catalogs, which an entry that moved between them is held to.
func pnpmDependency(entry *value, what, name string, baseEntry *value, others []*value, claims *claimSet) {
	needDict(entry, what)
	if entry.M.Len() != 2 || !entry.M.Has("specifier") || !entry.M.Has("version") {
		reject(what + " is not exactly a specifier and a version")
	}
	if !isRange(entry.M.Get("specifier")) {
		reject(what + " specifier " + q(entry.M.Get("specifier")) + " is not a semver range")
	}
	depVersion(entry.M.Get("version"), what+" version")
	version := beforeParen(entry.M.Get("version").S)
	mustSatisfy(version, entry.M.Get("specifier"), what)
	var counterpart *value
	switch {
	case !isNone(baseEntry):
		counterpart = baseEntry
	case len(others) > 0:
		counterpart = lowestEntry(others, what)
	}
	if counterpart != nil {
		before := beforeParen(core.PyStr(needDict(counterpart, "the base "+what).Get("version")))
		notLower(version, before, what)
	}
	if isNone(baseEntry) || version != beforeParen(core.PyStr(baseEntry.M.Get("version"))) {
		claims.add(name, version, entry.M.Get("specifier"))
	}
}

// pnpmChanged applies check(key, entry, base entry) to every entry that is new or differs.
func pnpmChanged(head, base *value, check func(key string, entry, baseEntry *value)) {
	hm := needDict(head, "a section")
	for _, key := range hm.Keys {
		entry := hm.Vals[key]
		var baseEntry *value
		if isMap(base) {
			baseEntry = base.M.Get(key)
			if base.M.Has(key) && canonEq(baseEntry, entry) {
				continue
			}
		}
		check(key, entry, baseEntry)
	}
}

var pnpmSections = []string{"dependencies", "devDependencies", "optionalDependencies"}

func pnpmImporter(path string, importer, baseImporter *value, claims *claimSet) {
	what := "importer " + q(path)
	if isNone(baseImporter) {
		reject(what + " is new")
	}
	needDict(importer, what)
	needDict(baseImporter, "the base "+what)
	keys := append([]string{}, importer.M.Keys...)
	for _, k := range baseImporter.M.Keys {
		if !importer.M.Has(k) {
			keys = append(keys, k)
		}
	}
	for _, key := range keys {
		if !core.InList(pnpmSections, key) && !sameKey(importer, baseImporter, key) {
			reject(what + " changes " + q(key))
		}
	}
	for _, key := range pnpmSections {
		if !importer.M.Has(key) {
			continue
		}
		dependency := func(name string, entry, baseEntry *value) {
			if !isName(str(name)) {
				reject(what + " " + key + " names " + q(name))
			}
			var others []*value
			for _, other := range pnpmSections {
				if other == key {
					continue
				}
				m := core.ItemsOf(baseImporter.M.Get(other))
				if m.Has(name) {
					others = append(others, m.Vals[name])
				}
			}
			pnpmDependency(entry, what+" "+key+" "+q(name), name, baseEntry, others, claims)
		}
		pnpmChanged(importer.M.Get(key), baseImporter.M.Get(key), dependency)
	}
}

func checkPNPM(baseText, headText string, todo *[]todoItem, claimsOut *[]func() []claim) {
	claims := newClaimSet()
	base := parseYAML(baseText, "base")
	head := parseYAML(headText, "head")
	for _, side := range []*value{base, head} {
		v := side.M.Get("lockfileVersion")
		if !isStr(v) || v.S != "9.0" || v.Style != core.StyleSingle {
			reject("the lockfileVersion is not 9.0")
		}
	}

	handled := []string{"importers", "packages", "snapshots", "overrides", "catalogs", "patchedDependencies"}
	keys := append([]string{}, base.M.Keys...)
	for _, k := range head.M.Keys {
		if !base.M.Has(k) {
			keys = append(keys, k)
		}
	}
	for _, key := range keys {
		if !core.InList(handled, key) && !sameKey(base, head, key) {
			reject("the top-level key " + q(key) + " changes")
		}
	}

	pkg := func(key string, entry, baseEntry *value) {
		what := "packages entry " + q(key)
		i := strings.LastIndex(key, "@")
		name, version := "", key
		if i >= 0 {
			name, version = key[:i], key[i+1:]
		}
		if !isName(str(name)) || !pnpmVersionFullRE.MatchString(version) {
			reject(what + " is not <name>@<semver>")
		}
		pnpmPackageEntry(name, version, entry, baseEntry, what, todo)
	}

	snapshot := func(key string, entry, baseEntry *value) {
		what := "snapshots entry " + q(key)
		named := pnpmNameAtRE.FindStringSubmatchIndex(key)
		if named == nil {
			reject(what + " is not <name>@<semver>")
		}
		rest := key[named[1]:]
		depVersion(str(rest), what)
		prefix := beforeParen(key)
		var siblings []*value
		bs := core.ItemsOf(base.M.Get("snapshots"))
		for _, otherKey := range bs.Keys {
			if o := bs.Vals[otherKey]; beforeParen(otherKey) == prefix && isMap(o) {
				siblings = append(siblings, o)
			}
		}
		pnpmSnapshotEntry(key[named[2]:named[3]], beforeParen(rest), entry, baseEntry, siblings, what, todo, claims)
	}

	override := func(key string, v, _ *value) {
		if !isRange(v) {
			reject("override " + q(key) + " is " + q(v) + ", not a semver range")
		}
	}

	catalog := func(name string, entries, baseEntries *value) {
		em := needDict(entries, "catalog "+q(name))
		for _, dep := range em.Keys {
			entry := em.Vals[dep]
			if isMap(baseEntries) && pyEq(baseEntries.M.Get(dep), entry) && baseEntries.M.Has(dep) {
				continue
			}
			if !isName(str(dep)) {
				reject("catalog " + q(name) + " names " + q(dep))
			}
			var same *value
			if isMap(baseEntries) {
				same = baseEntries.M.Get(dep)
			}
			var others []*value
			cs := core.ItemsOf(base.M.Get("catalogs"))
			for _, otherName := range cs.Keys {
				other := cs.Vals[otherName]
				if otherName != name && isMap(other) && other.M.Has(dep) {
					others = append(others, other.M.Get(dep))
				}
			}
			pnpmDependency(entry, "catalog "+q(name)+" "+q(dep), dep, same, others, claims)
		}
	}

	if head.M.Has("packages") {
		pnpmChanged(head.M.Get("packages"), base.M.Get("packages"), pkg)
	}
	if head.M.Has("snapshots") {
		pnpmChanged(head.M.Get("snapshots"), base.M.Get("snapshots"), snapshot)
	}
	if head.M.Has("importers") {
		pnpmChanged(head.M.Get("importers"), base.M.Get("importers"), func(path string, importer, baseImporter *value) {
			pnpmImporter(path, importer, baseImporter, claims)
		})
	}
	if head.M.Has("overrides") {
		pnpmChanged(head.M.Get("overrides"), base.M.Get("overrides"), override)
	}
	if head.M.Has("catalogs") {
		pnpmChanged(head.M.Get("catalogs"), base.M.Get("catalogs"), catalog)
	}
	if head.M.Has("patchedDependencies") {
		pnpmChanged(head.M.Get("patchedDependencies"), base.M.Get("patchedDependencies"),
			func(key string, _, _ *value) { reject("patchedDependencies gains " + q(key)) })
	}

	// Rule 6, once the manifests have said what each snapshot asks for: a package at a version the
	// base did not hold must be asked for by something, and be the newest the ranges allow.
	*claimsOut = append(*claimsOut, func() []claim {
		basePackages := core.ItemsOf(base.M.Get("packages"))
		held := map[string]map[string]bool{}
		split := func(key string) (string, string) {
			if i := strings.LastIndex(key, "@"); i >= 0 {
				return key[:i], key[i+1:]
			}
			return "", key
		}
		for _, key := range basePackages.Keys {
			name, version := split(key)
			if held[name] == nil {
				held[name] = map[string]bool{}
			}
			held[name][version] = true
		}
		headPackages := core.ItemsOf(head.M.Get("packages"))
		for _, key := range headPackages.Keys {
			name, version := split(key)
			if !basePackages.Has(key) && !claims.has(name, version) {
				reject("packages entry " + q(key) + " is new and nothing depends on it")
			}
		}
		var out []claim
		for _, k := range claims.keys {
			var h []string
			for v := range held[k[0]] {
				h = append(h, v)
			}
			sortStrings(h)
			out = append(out, claim{k[0], k[1], claims.ranges[k], h, k[0] + "@" + k[1]})
		}
		return out
	})
}
