package lockfilecheck

import (
	"sort"
	"strings"

	"github.com/nicodarge/go-tools/internal/core"
)

var (
	npmBools     = []string{"dev", "optional", "devOptional", "peer"}
	npmDepMaps   = []string{"dependencies", "optionalDependencies", "peerDependencies"}
	npmFixed     = []string{"dependencies", "optionalDependencies", "peerDependencies", "peerDependenciesMeta", "bin"}
	npmRootMaps  = []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"}
	npmEntryKeys = []string{"version", "resolved", "integrity", "peerDependenciesMeta", "bin",
		"dev", "optional", "devOptional", "peer",
		"license", "engines", "cpu", "os", "libc", "funding", "deprecated",
		"dependencies", "optionalDependencies", "peerDependencies"}
	legacyKeys = []string{"version", "resolved", "integrity", "requires", "dependencies",
		"dev", "optional", "devOptional", "peer"}
)

type todoItem struct {
	name, version string
	compare       func(manifest *value)
}

type claim struct {
	name, version string
	ranges        []*value
	held          []string
	what          string
}

func loadJSON(text []byte, which string) *value {
	v, ok := core.ParseJSON(text, false)
	if !ok {
		reject("the " + which + " file is not JSON")
	}
	needDict(v, "the "+which+" file")
	core.AsciiOnly(v, "", false, 0)
	return v
}

func fundingOK(v *value) bool {
	switch core.KindOf(v) {
	case core.Str:
		return true
	case core.Map:
		for _, k := range v.M.Keys {
			if k != "type" && k != "url" {
				return false
			}
			if !isStr(v.M.Vals[k]) {
				return false
			}
		}
		return true
	case core.List:
		for _, item := range v.L {
			if !(isStr(item) || isMap(item)) || !fundingOK(item) {
				return false
			}
		}
		return true
	}
	return false
}

func npmMetadata(entry *value, what string) {
	plainMetadata(entry, what)
	if mhas(entry, "license") && !isStr(entry.M.Get("license")) {
		reject(what + " license is not a string")
	}
	if mhas(entry, "funding") && !fundingOK(entry.M.Get("funding")) {
		reject(what + " funding is not plain metadata")
	}
}

func registryTarball(name, version string) string {
	return registryURL + "/" + name + "/-/" + name[strings.LastIndex(name, "/")+1:] + "-" + version + ".tgz"
}

func isTrue(v *value) bool { return core.KindOf(v) == core.Bool && v.B }

func isBool(v *value) bool { return core.KindOf(v) == core.Bool }

func npmResolution(name string, entry, baseEntry *value, what string, allowed []string) {
	core.NeedOnlyKeys(entry.M, allowed, what)
	version := entry.M.Get("version")
	if !isStr(version) || !npmVersionRE.MatchString(version.S) {
		reject(what + " has no plain semver version")
	}
	if !isNone(baseEntry) {
		notLower(version, mget(baseEntry, "version"), what)
	}
	if !pyEq(entry.M.Get("resolved"), str(registryTarball(name, version.S))) {
		reject(what + " resolved is not the registry tarball of " + q(name) + " " + q(version.S))
	}
	if !isIntegrity(entry.M.Get("integrity")) {
		reject(what + " has no integrity hash")
	}
	for _, key := range npmBools {
		if entry.M.Has(key) && !isTrue(entry.M.Get(key)) {
			reject(what + " " + key + " is not true")
		}
	}
	if !isNone(baseEntry) && pyEq(mget(baseEntry, "version"), version) &&
		!canonEq(mget(baseEntry, "integrity"), entry.M.Get("integrity")) {
		reject(what + " changes integrity under an unchanged version")
	}
}

// npmFixedCheck: a published package is immutable, so what it depends on never changes.
func npmFixedCheck(entry, baseEntry *value, keys []string, what string) {
	if !isNone(baseEntry) && pyEq(mget(baseEntry, "version"), mget(entry, "version")) {
		for _, key := range keys {
			if !sameKey(entry, baseEntry, key) {
				reject(what + " changes " + key + " under an unchanged version")
			}
		}
	}
}

func npmEntry(name string, entry, baseEntry *value, what string, todo *[]todoItem) {
	needDict(entry, what)
	npmResolution(name, entry, baseEntry, what, npmEntryKeys)
	npmMetadata(entry, what)
	if entry.M.Has("bin") {
		stringMap(entry.M.Get("bin"), what+" bin")
	}
	for _, key := range npmDepMaps {
		if entry.M.Has(key) {
			dependencyMap(entry.M.Get(key), what+" "+key)
		}
	}
	if entry.M.Has("peerDependenciesMeta") {
		meta := needDict(entry.M.Get("peerDependenciesMeta"), what+" peerDependenciesMeta")
		for _, dep := range meta.Keys {
			m := meta.Vals[dep]
			bad := !isName(str(dep)) || !isMap(m)
			if !bad {
				for _, k := range m.M.Keys {
					if k != "optional" || !isBool(m.M.Vals[k]) {
						bad = true
					}
				}
			}
			if bad {
				reject(what + " peerDependenciesMeta " + q(dep) + " is not an optional flag")
			}
		}
	}
	npmFixedCheck(entry, baseEntry, npmFixed, what)
	*todo = append(*todo, todoItem{name, entry.M.Get("version").S, func(manifest *value) {
		npmAgainst(name, entry, manifest, what)
	}})
}

func strMapEq(a *core.OMap, b *core.OMap) bool { return pyEq(core.MapValue(a), core.MapValue(b)) }

func npmAgainst(name string, entry, manifest *value, what string) {
	manifestIntegrity(manifest, entry.M.Get("integrity"), what)
	manifestNoInstall(manifest, what)
	optional := manifestMap(manifest, "optionalDependencies", what)
	// npm keeps an optional dependency out of `dependencies` in the lockfile
	// though the registry manifest lists it in both.
	plain := core.NewMap()
	deps := manifestMap(manifest, "dependencies", what)
	for _, k := range deps.Keys {
		if !optional.Has(k) {
			plain.Set(k, deps.Vals[k])
		}
	}
	published := []struct {
		key string
		m   *core.OMap
	}{
		{"dependencies", plain},
		{"optionalDependencies", optional},
		{"peerDependencies", manifestMap(manifest, "peerDependencies", what)},
	}
	for _, p := range published {
		if !strMapEq(core.ItemsOf(entry.M.Get(p.key)), p.m) {
			reject(what + " " + p.key + " are not the ones the registry publishes")
		}
	}
	if !canonEq(orEmpty(entry.M.Get("peerDependenciesMeta")), orEmpty(mget(manifest, "peerDependenciesMeta"))) {
		reject(what + " peerDependenciesMeta is not the one the registry publishes")
	}
	if !strMapEq(core.ItemsOf(entry.M.Get("bin")), manifestBin(manifest, name, what)) {
		reject(what + " bin is not the one the registry publishes")
	}
}

func npmRoot(baseRoot, headRoot *value) {
	needDict(baseRoot, "the base root entry")
	needDict(headRoot, "the root entry")
	keys := append([]string{}, baseRoot.M.Keys...)
	for _, k := range headRoot.M.Keys {
		if !baseRoot.M.Has(k) {
			keys = append(keys, k)
		}
	}
	for _, key := range keys {
		if !core.InList(npmRootMaps, key) {
			if !sameKey(baseRoot, headRoot, key) {
				reject("the root entry changes " + q(key))
			}
			continue
		}
		if !headRoot.M.Has(key) {
			continue
		}
		baseMap := baseRoot.M.Get(key)
		hm := needDict(headRoot.M.Get(key), "the root "+key)
		for _, dep := range hm.Keys {
			spec := hm.Vals[dep]
			if isMap(baseMap) && baseMap.M.Has(dep) && canonEq(baseMap.M.Get(dep), spec) {
				continue
			}
			if !isName(str(dep)) || !isRange(spec) {
				reject("the root " + key + " sets " + q(dep) + " to " + q(spec) + ", not a semver range")
			}
		}
	}
}

func npmLegacy(headTree, baseTree *value, todo *[]todoItem, headPackages *core.OMap, trail []string) {
	needDict(headTree, "the legacy dependency tree")
	for _, name := range headTree.M.Keys {
		node := headTree.M.Vals[name]
		var baseNode *value
		if isMap(baseTree) {
			baseNode = baseTree.M.Get(name)
		}
		if !isNone(baseNode) && !isMap(baseNode) {
			reject("the legacy entry " + q(name) + " is not a mapping in the base")
		}
		what := "legacy entry " + q(name)
		needDict(node, what)
		flat := func(n *value) *value {
			if isNone(n) {
				return nil
			}
			m := core.NewMap()
			for _, k := range n.M.Keys {
				if k != "dependencies" && k != "requires" {
					m.Set(k, n.M.Vals[k])
				}
			}
			return core.MapValue(m)
		}
		changed := !canonEq(flat(node), flat(baseNode))
		if !changed {
			changed = !canonEq(node.M.Get("requires"), mget(baseNode, "requires"))
		}
		if changed {
			if !isName(str(name)) {
				reject(what + " is not a package name")
			}
			npmResolution(name, node, baseNode, what, legacyKeys)
			parts := append(append([]string{}, trail...), name)
			mirror := headPackages.Get("node_modules/" + strings.Join(parts, "/node_modules/"))
			if !isMap(mirror) || !pyEq(mirror.M.Get("version"), node.M.Get("version")) ||
				!pyEq(mirror.M.Get("integrity"), node.M.Get("integrity")) {
				reject(what + " is not mirrored by an entry of packages")
			}
			if node.M.Has("requires") {
				dependencyMap(node.M.Get("requires"), what+" requires")
			}
			npmFixedCheck(node, baseNode, []string{"requires"}, what)
			*todo = append(*todo, todoItem{name, node.M.Get("version").S, func(manifest *value) {
				declared := core.NewMap()
				for _, k := range []string{"dependencies", "optionalDependencies"} {
					m := manifestMap(manifest, k, what)
					for _, dk := range m.Keys {
						declared.Set(dk, m.Vals[dk])
					}
				}
				req := core.ItemsOf(node.M.Get("requires"))
				for _, dep := range req.Keys {
					if !pyEq(declared.Get(dep), req.Vals[dep]) {
						reject(what + " requires " + q(dep) + ", which the registry does not publish")
					}
				}
			}})
		}
		if node.M.Has("dependencies") {
			var baseDeps *value
			if !isNone(baseNode) {
				baseDeps = baseNode.M.Get("dependencies")
			}
			npmLegacy(node.M.Get("dependencies"), baseDeps, todo, headPackages, append(append([]string{}, trail...), name))
		}
	}
}

// npmResolve: where node finds `name` from the package at `location`, nearest node_modules first.
func npmResolve(packages *core.OMap, location, name string) (string, bool) {
	for {
		candidate := "node_modules/" + name
		if location != "" {
			candidate = location + "/node_modules/" + name
		}
		if packages.Has(candidate) {
			return candidate, true
		}
		if location == "" {
			return "", false
		}
		cut := strings.LastIndex(location, "node_modules/")
		if cut > 0 {
			location = strings.TrimRight(location[:cut], "/")
		} else {
			location = ""
		}
	}
}

type depKind struct {
	name     string
	required bool
}

func npmKinds(key string) []depKind {
	if key == "" {
		return []depKind{{"dependencies", true}, {"devDependencies", true}, {"optionalDependencies", false}, {"peerDependencies", false}}
	}
	return []depKind{{"dependencies", true}, {"optionalDependencies", false}, {"peerDependencies", false}}
}

// npmEdges: every dependency that is new, whose range changed, or that now resolves to something
// else must be satisfied by what it resolves to. This is what stops a nested copy being added, or
// one removed, so that a package picks up a version outside its range.
func npmEdges(basePackages, headPackages *core.OMap) {
	for _, key := range headPackages.Keys {
		entry := headPackages.Vals[key]
		baseEntry := basePackages.Get(key)
		for _, kind := range npmKinds(key) {
			deps := core.GetItems(entry, kind.name)
			for _, dep := range deps.Keys {
				spec := deps.Vals[dep]
				target, hasTarget := npmResolve(headPackages, key, dep)
				before, hasBefore := npmResolve(basePackages, key, dep)
				asked := isNone(baseEntry) || !pyEq(core.GetItems(baseEntry, kind.name).Get(dep), spec)
				moved := asked || target != before || hasTarget != hasBefore
				if !moved && hasTarget {
					var baseVersion *value
					if bt := basePackages.Get(target); bt != nil || basePackages.Has(target) {
						baseVersion = mget(bt, "version")
					}
					moved = !canonEq(mget(headPackages.Vals[target], "version"), baseVersion)
				}
				if !moved {
					continue
				}
				what := "entry " + q(key) + " " + kind.name + " " + q(dep)
				if !hasTarget {
					if kind.required {
						reject(what + " resolves to nothing in the lockfile")
					}
					continue
				}
				headVersion := mget(headPackages.Vals[target], "version")
				mustSatisfy(headVersion, spec, what)
				// A package that is new, or whose range for this dependency changed in this very
				// change (the range then comes from its registry manifest), may ask for less than
				// the base resolved and force a lower version. One that did not change gets no such
				// leave: its range is the base's, which overrides, legacy-peer-deps and force can
				// leave out of step with what is installed.
				if hasBefore {
					baseVersion := mget(basePackages.Vals[before], "version")
					if !asked || satisfies(baseVersion, spec) {
						notLower(headVersion, baseVersion, what)
					}
				}
			}
		}
	}
}

// npmClaims is rule 6 for npm: a claim for every entry that is new or at a new version. The ranges
// are those of every entry in the head that resolves to it.
func npmClaims(basePackages, headPackages *core.OMap) []claim {
	asking := map[string][]*value{}
	for _, key := range headPackages.Keys {
		entry := headPackages.Vals[key]
		for _, kind := range npmKinds(key) {
			deps := core.GetItems(entry, kind.name)
			for _, dep := range deps.Keys {
				if target, ok := npmResolve(headPackages, key, dep); ok {
					asking[target] = append(asking[target], deps.Vals[dep])
				}
			}
		}
	}
	var claims []claim
	for _, key := range headPackages.Keys {
		entry := headPackages.Vals[key]
		baseEntry := basePackages.Get(key)
		if !isNone(baseEntry) && pyEq(mget(baseEntry, "version"), mget(entry, "version")) {
			continue
		}
		m := npmKeyRE.FindStringSubmatch(key)
		if m == nil {
			core.Crash("AttributeError")
		}
		name := m[1]
		what := "entry " + q(key)
		if len(asking[key]) == 0 {
			reject(what + " is new and nothing depends on it")
		}
		heldSet := map[string]bool{}
		for _, otherKey := range basePackages.Keys {
			other := basePackages.Vals[otherKey]
			if otherKey == "" {
				continue
			}
			om := npmKeyRE.FindStringSubmatch(otherKey)
			if om != nil && om[1] == name {
				if ov := mget(other, "version"); isStr(ov) {
					heldSet[ov.S] = true
				}
			}
		}
		held := make([]string, 0, len(heldSet))
		for v := range heldSet {
			held = append(held, v)
		}
		sort.Strings(held)
		claims = append(claims, claim{name, entry.M.Get("version").S, asking[key], held, what})
	}
	return claims
}

func checkNPM(baseText, headText []byte, todo *[]todoItem, claims *[]func() []claim) {
	base := loadJSON(baseText, "base")
	head := loadJSON(headText, "head")
	lv := head.M.Get("lockfileVersion")
	if core.KindOf(lv) != core.Num || !lv.Int || (lv.S != "2" && lv.S != "3") ||
		!canonEq(lv, base.M.Get("lockfileVersion")) {
		reject("the lockfileVersion is not 2 or 3, or differs from the base")
	}
	version2 := lv.S == "2"
	rest := func(side *value) *value {
		m := core.NewMap()
		for _, k := range side.M.Keys {
			if k == "packages" || (version2 && k == "dependencies") {
				continue
			}
			m.Set(k, side.M.Vals[k])
		}
		return core.MapValue(m)
	}
	if !canonEq(rest(head), rest(base)) {
		reject("a top-level key outside the package entries changes")
	}

	headPackages := needDict(head.M.Get("packages"), "packages")
	basePackages := needDict(base.M.Get("packages"), "the base packages")
	if !headPackages.Has("") || !basePackages.Has("") {
		reject("the root entry is missing")
	}

	for _, key := range headPackages.Keys {
		entry := headPackages.Vals[key]
		baseEntry := basePackages.Get(key)
		if !isNone(baseEntry) && canonEq(baseEntry, entry) {
			continue
		}
		if key == "" {
			npmRoot(baseEntry, entry)
			continue
		}
		var found []string
		if len(key) <= 512 {
			found = npmKeyRE.FindStringSubmatch(key)
		}
		if found == nil {
			reject("the entry " + q(key) + " is not a node_modules package")
		}
		if !isNone(baseEntry) {
			needDict(baseEntry, "the base entry "+q(key))
		}
		npmEntry(found[1], entry, baseEntry, "entry "+q(key), todo)
	}

	npmEdges(basePackages, headPackages)
	cl := npmClaims(basePackages, headPackages)
	*claims = append(*claims, func() []claim { return cl })

	if version2 {
		legacy := head.M.Get("dependencies")
		if !head.M.Has("dependencies") {
			legacy = core.MapValue(core.NewMap())
		}
		npmLegacy(legacy, base.M.Get("dependencies"), todo, headPackages, nil)
	}
}
