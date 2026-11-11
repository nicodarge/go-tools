package lockfilecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures here are synthetic: a package "foo" that depends on "bar" (npm), and "zod" (pnpm).

var (
	intC = "sha512-" + strings.Repeat("C", 86) + "=="
	intD = "sha512-" + strings.Repeat("D", 86) + "=="
	intE = "sha512-" + strings.Repeat("E", 86) + "=="
)

type object = map[string]any

// registry is what the recorded registry holds: manifests by "name@version" and packuments by name.
type registry struct {
	manifests  map[string]object
	packuments map[string]object
}

func manifestOf(name, version, integrity string, extra object) object {
	m := object{"name": name, "version": version, "dist": object{"integrity": integrity}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func packumentOf(name string, versions ...string) object {
	times, vs := object{}, object{}
	for _, v := range versions {
		times[v] = longAgo
		vs[v] = object{}
	}
	return object{"name": name, "time": times, "versions": vs}
}

// verdict runs the lockfile check through Main with the recorded registry and returns its exit
// code and first output line.
func verdict(t *testing.T, path, base, head string, reg registry) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := runMain(t, &out, path, base, head, reg, nil)
	first, _, _ := strings.Cut(out.String(), "\n")
	return code, first
}

// runMain writes the registry and the files to a directory and runs Main on them; edit may change
// the directory before the run.
func runMain(t *testing.T, out io.Writer, path, base, head string, reg registry, edit func(dir string)) int {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	manifests := filepath.Join(dir, "manifests")
	if err := os.Mkdir(manifests, 0o700); err != nil {
		t.Fatal(err)
	}
	for key, m := range reg.manifests {
		data, _ := json.Marshal(m)
		write("manifests/"+strings.ReplaceAll(key, "/", "%2F")+".json", data)
	}
	for name, p := range reg.packuments {
		data, _ := json.Marshal(p)
		write("manifests/"+strings.ReplaceAll(name, "/", "%2F")+".packument.json", data)
	}
	basePath, headPath := write("base", []byte(base)), write("head", []byte(head))
	if edit != nil {
		edit(dir)
	}
	return Main([]string{"--manifests", manifests, "--before", "2030-01-01T00:00:00Z", path, basePath, headPath}, out)
}

type verdictCase struct {
	label string
	head  string
	reg   *registry // nil for the registry of the clean bump
	want  string    // "" for PASS, else a substring of the refusal
}

func runCases(t *testing.T, path, base string, defaultReg registry, cases []verdictCase) {
	t.Helper()
	for _, c := range cases {
		reg := defaultReg
		if c.reg != nil {
			reg = *c.reg
		}
		code, line := verdict(t, path, base, c.head, reg)
		switch {
		case c.want == "" && (code != 0 || line != "PASS"):
			t.Errorf("%s: %d %q, want PASS", c.label, code, line)
		case c.want != "" && (code != 1 || !strings.HasPrefix(line, "FAIL: ") || !strings.Contains(line, c.want)):
			t.Errorf("%s: %d %q, want a refusal with %q", c.label, code, line, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// npm

func lockEntry(name, version, integrity string, extra object) object {
	e := object{"version": version, "integrity": integrity,
		"resolved": fmt.Sprintf("%s/%s/-/%s-%s.tgz", registryURL, name, name, version)}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func npmLock(version int, foo object, legacyFoo object) string {
	lock := object{"name": "demo", "version": "1.0.0", "lockfileVersion": version, "requires": true,
		"packages": object{
			"":                 object{"name": "demo", "version": "1.0.0", "dependencies": object{"foo": "^1.0.0"}},
			"node_modules/foo": foo,
			"node_modules/bar": lockEntry("bar", "1.0.0", intB, nil),
		}}
	if version == 2 {
		lock["dependencies"] = object{
			"foo": legacyFoo,
			"bar": lockEntry("bar", "1.0.0", intB, nil),
		}
	}
	data, _ := json.MarshalIndent(lock, "", "  ")
	return string(data)
}

func fooEntry(version, integrity string, extra object) object {
	e := lockEntry("foo", version, integrity, object{"dependencies": object{"bar": "^1.0.0"}})
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func npmRegistry() registry {
	return registry{
		manifests:  map[string]object{"foo@1.0.1": manifestOf("foo", "1.0.1", intE, object{"dependencies": object{"bar": "^1.0.0"}})},
		packuments: map[string]object{"foo": packumentOf("foo", "1.0.0", "1.0.1")},
	}
}

func TestNPMThroughMain(t *testing.T) {
	base := npmLock(3, fooEntry("1.0.0", intA, nil), nil)
	good := npmRegistry()
	withManifest := func(extra object) *registry {
		r := npmRegistry()
		for k, v := range extra {
			r.manifests["foo@1.0.1"][k] = v
		}
		return &r
	}
	headOf := func(foo object, edit func(lock object)) string {
		var lock object
		_ = json.Unmarshal([]byte(npmLock(3, foo, nil)), &lock)
		if edit != nil {
			edit(lock)
		}
		data, _ := json.MarshalIndent(lock, "", "  ")
		return string(data)
	}
	bumped := fooEntry("1.0.1", intE, nil)
	root := func(lock object) object { return lock["packages"].(object)[""].(object) }
	packages := func(lock object) object { return lock["packages"].(object) }

	runCases(t, "package-lock.json", base, good, []verdictCase{
		{"no change", base, nil, ""},
		{"a bump the registry publishes", headOf(bumped, nil), nil, ""},
		{"a bump with plain funding metadata", headOf(fooEntry("1.0.1", intE, object{"funding": object{"type": "github", "url": "https://example.test/f"}}), nil), nil, ""},
		{"a bump with funding as a list of strings and mappings", headOf(fooEntry("1.0.1", intE, object{"funding": []any{"https://example.test/f", object{"url": "https://example.test/g"}}}), nil), nil, ""},
		{"a package leaving the tree", headOf(bumped, func(lock object) { delete(packages(lock), "node_modules/bar") }), nil, "resolves to nothing in the lockfile"},

		// where the tarball comes from
		{"resolved on another host", headOf(fooEntry("1.0.1", intE, object{"resolved": "https://evil.test/foo/-/foo-1.0.1.tgz"}), nil), nil, "resolved is not the registry tarball"},
		{"resolved as a git URL", headOf(fooEntry("1.0.1", intE, object{"resolved": "git+ssh://git@evil.test/foo.git#abc"}), nil), nil, "resolved is not the registry tarball"},
		{"resolved over plain http", headOf(fooEntry("1.0.1", intE, object{"resolved": "http://registry.npmjs.org/foo/-/foo-1.0.1.tgz"}), nil), nil, "resolved is not the registry tarball"},
		{"resolved for another version", headOf(fooEntry("1.0.1", intE, object{"resolved": registryURL + "/foo/-/foo-1.0.0.tgz"}), nil), nil, "resolved is not the registry tarball"},
		{"resolved missing", headOf(func() object { e := fooEntry("1.0.1", intE, nil); delete(e, "resolved"); return e }(), nil), nil, "resolved is not the registry tarball"},

		// what the tarball is
		{"an integrity swapped for another hash", headOf(fooEntry("1.0.1", intD, nil), nil), nil, "integrity is not the one the registry publishes"},
		{"an integrity that is not a hash", headOf(fooEntry("1.0.1", "md5-AAAA", nil), nil), nil, "has no integrity hash"},
		{"an integrity changed under an unchanged version", headOf(bumped, func(lock object) {
			packages(lock)["node_modules/bar"] = lockEntry("bar", "1.0.0", intC, nil)
		}), nil, "changes integrity under an unchanged version"},
		{"a dependency changed under an unchanged version", headOf(fooEntry("1.0.0", intA, object{"dependencies": object{"bar": "^1.0.0", "evil": "^1.0.0"}}), nil), nil, "changes dependencies under an unchanged version"},

		// what the registry says about the version
		{"an install script in the manifest", headOf(bumped, nil), withManifest(object{"scripts": object{"postinstall": "node x.js"}}), "declares an install script"},
		{"hasInstallScript in the manifest", headOf(bumped, nil), withManifest(object{"hasInstallScript": true}), "declares an install script"},
		{"gypfile in the manifest", headOf(bumped, nil), withManifest(object{"gypfile": true}), "declares an install script"},
		{"a manifest with other dependencies", headOf(bumped, nil), withManifest(object{"dependencies": object{"bar": "^1.0.0", "extra": "^1.0.0"}}), "dependencies are not the ones the registry publishes"},
		{"a manifest with a bin the entry lacks", headOf(bumped, nil), withManifest(object{"bin": "cli.js"}), "bin is not the one the registry publishes"},

		// what the entry may carry
		{"a key nobody thought of", headOf(fooEntry("1.0.1", intE, object{"hasInstallScript": true}), nil), nil, "carries"},
		{"funding with an extra key", headOf(fooEntry("1.0.1", intE, object{"funding": object{"url": "https://example.test/f", "run": "x"}}), nil), nil, "funding is not plain metadata"},
		{"funding as a number", headOf(fooEntry("1.0.1", intE, object{"funding": 5}), nil), nil, "funding is not plain metadata"},
		{"funding as a list holding a number", headOf(fooEntry("1.0.1", intE, object{"funding": []any{5}}), nil), nil, "funding is not plain metadata"},
		{"a license that is not a string", headOf(fooEntry("1.0.1", intE, object{"license": []any{"MIT"}}), nil), nil, "license is not a string"},
		{"dev set to a string", headOf(fooEntry("1.0.1", intE, object{"dev": "true"}), nil), nil, "dev is not true"},

		// the root entry
		{"the root name changed", headOf(bumped, func(lock object) { root(lock)["name"] = "other" }), nil, "the root entry changes"},
		{"a root dependency pointing at git", headOf(bumped, func(lock object) { root(lock)["dependencies"] = object{"foo": "git+https://evil.test/foo.git"} }), nil, "not a semver range"},
		{"a root dependency pointing at a file", headOf(bumped, func(lock object) { root(lock)["dependencies"] = object{"foo": "file:../foo"} }), nil, "not a semver range"},
		{"a root dependency pointing at a tag", headOf(bumped, func(lock object) { root(lock)["dependencies"] = object{"foo": "latest"} }), nil, "not a semver range"},
		{"a root dependency on a name that is not one", headOf(bumped, func(lock object) { root(lock)["dependencies"] = object{"foo": "^1.0.0", "../x": "^1.0.0"} }), nil, "not a semver range"},
		{"a root map that is not a mapping", headOf(bumped, func(lock object) { root(lock)["devDependencies"] = []any{"x"} }), nil, "is not a mapping"},
	})

	// A tree the way lockfileVersion 2 also keeps it.
	legacyBase := npmLock(2, fooEntry("1.0.0", intA, nil), lockEntry("foo", "1.0.0", intA, object{"requires": object{"bar": "^1.0.0"}}))
	legacyHead := func(foo, legacyFoo object) string { return npmLock(2, foo, legacyFoo) }
	legacyBumped := lockEntry("foo", "1.0.1", intE, object{"requires": object{"bar": "^1.0.0"}})
	runCases(t, "package-lock.json", legacyBase, good, []verdictCase{
		{"legacy: no change", legacyBase, nil, ""},
		{"legacy: a bump mirrored by packages", legacyHead(bumped, legacyBumped), nil, ""},
		{"legacy: an entry bumped and not mirrored by packages", legacyHead(fooEntry("1.0.0", intA, nil), legacyBumped), nil, "is not mirrored by an entry of packages"},
		{"legacy: a mirror at another version", legacyHead(fooEntry("1.0.1", intE, nil), lockEntry("foo", "1.0.2", intE, object{"requires": object{"bar": "^1.0.0"}})), nil, "is not mirrored by an entry of packages"},
		{"legacy: a mirror with another integrity", legacyHead(fooEntry("1.0.1", intD, nil), legacyBumped), nil, "is not mirrored by an entry of packages"},
		{"legacy: requires the registry does not publish", legacyHead(bumped, lockEntry("foo", "1.0.1", intE, object{"requires": object{"bar": "^9.0.0"}})), nil, "requires"},
		{"legacy: resolved on another host", legacyHead(bumped, lockEntry("foo", "1.0.1", intE, object{"requires": object{"bar": "^1.0.0"}, "resolved": "https://evil.test/foo.tgz"})), nil, "resolved is not the registry tarball"},
		{"legacy: a key nobody thought of", legacyHead(bumped, lockEntry("foo", "1.0.1", intE, object{"requires": object{"bar": "^1.0.0"}, "bundled": true})), nil, "carries"},
		{"legacy: a tree that is not a mapping", strings.Replace(legacyHead(bumped, legacyBumped), `"dependencies": {`, `"dependencies": [`, 1), nil, "not JSON"},
	})

	// The file itself.
	for _, c := range []struct{ label, head, want string }{
		{"a duplicate key", strings.Replace(headOf(bumped, nil), `"requires": true`, `"requires": true, "requires": true`, 1), "duplicate key"},
		{"a forbidden key", strings.Replace(headOf(bumped, nil), `"requires": true`, `"__proto__": 1, "requires": true`, 1), "the key __proto__"},
		{"non-ASCII text", strings.Replace(headOf(bumped, nil), `"name": "demo"`, "\"name\": \"démo\"", 1), "non-ASCII"},
		{"not JSON", "{", "not JSON"},
		{"NaN", strings.Replace(headOf(bumped, nil), `"requires": true`, `"requires": NaN`, 1), "non-finite"},
	} {
		code, line := verdict(t, "package-lock.json", base, c.head, good)
		if code != 1 || !strings.Contains(line, c.want) {
			t.Errorf("%s: %d %q, want a refusal with %q", c.label, code, line, c.want)
		}
	}
	if code, line := verdict(t, "yarn.lock", base, base, good); code != 1 || !strings.Contains(line, "is not a lockfile this knows") {
		t.Errorf("another lockfile: %d %q", code, line)
	}
}

// ---------------------------------------------------------------------------
// pnpm

const pnpmBase = `lockfileVersion: '9.0'

settings:
  autoInstallPeers: true
  excludeLinksFromLockfile: false

importers:

  .:
    dependencies:
      zod:
        specifier: ^4.4.3
        version: 4.6.5

packages:

  tool@1.0.0:
    resolution: {integrity: INT_C}
    hasBin: true

  zod@4.6.5:
    resolution: {integrity: INT_A}

snapshots:

  tool@1.0.0: {}

  zod@4.6.5: {}
`

func pnpmText(text string) string {
	return strings.NewReplacer("INT_A", intA, "INT_B", intB, "INT_C", intC, "INT_D", intD, "INT_E", intE).Replace(text)
}

func TestPNPMThroughMain(t *testing.T) {
	base := pnpmText(pnpmBase)
	zodOld := "  zod@4.6.5:\n    resolution: {integrity: " + intA + "}\n"
	zodNew := "  zod@4.6.6:\n    resolution: {integrity: " + intE + "}\n"
	bump := func(text string) string {
		return strings.ReplaceAll(strings.Replace(text, zodOld, zodNew, 1), "4.6.5", "4.6.6")
	}
	head := bump(base)
	// with extends the new package's entry.
	with := func(extra string) string { return strings.Replace(head, zodNew, zodNew+extra, 1) }
	reg := registry{
		manifests:  map[string]object{"zod@4.6.6": manifestOf("zod", "4.6.6", intE, nil)},
		packuments: map[string]object{"zod": packumentOf("zod", "4.6.5", "4.6.6")},
	}
	wrongIntegrity := registry{
		manifests:  map[string]object{"zod@4.6.6": manifestOf("zod", "4.6.6", intD, nil)},
		packuments: reg.packuments,
	}
	installScript := registry{
		manifests:  map[string]object{"zod@4.6.6": manifestOf("zod", "4.6.6", intE, object{"scripts": object{"install": "node x.js"}})},
		packuments: reg.packuments,
	}
	more := registry{
		manifests: map[string]object{"zod@4.6.6": reg.manifests["zod@4.6.6"],
			"extra@1.0.0": manifestOf("extra", "1.0.0", intD, nil),
			"tool@1.0.0":  manifestOf("tool", "1.0.0", intC, object{"bin": object{"tool": "cli.js"}})},
		packuments: map[string]object{"zod": reg.packuments["zod"], "extra": packumentOf("extra", "1.0.0"), "tool": packumentOf("tool", "1.0.0")},
	}
	extraPeer := registry{
		manifests:  map[string]object{"zod@4.6.6": manifestOf("zod", "4.6.6", intE, object{"peerDependencies": object{"tool": "^1.0.0"}})},
		packuments: reg.packuments,
	}

	runCases(t, "pnpm-lock.yaml", base, reg, []verdictCase{
		{"no change", base, nil, ""},
		{"a clean bump", head, nil, ""},
		{"a deprecation message in double quotes", with("    deprecated: \"gone: see the notes\"\n"), nil, ""},
		{"engines, cpu and os metadata", with("    engines: {node: '>=18'}\n    cpu: [x64]\n    os: [linux]\n"), nil, ""},

		// the registry
		{"a wrong integrity", head, &wrongIntegrity, "integrity is not the one the registry publishes"},
		{"an install script in the manifest", head, &installScript, "declares an install script"},
		{"a peer dependency the entry does not carry", head, &extraPeer, "peerDependencies are not the ones the registry publishes"},
		{"an integrity swapped under an unchanged version", strings.Replace(base, intA, intD, 1), nil, "changes its integrity under an unchanged version"},
		{"a version the registry never published", head, &registry{manifests: map[string]object{}, packuments: reg.packuments}, "no recorded manifest"},
		{"an older version when a newer one exists", head, &registry{manifests: reg.manifests, packuments: map[string]object{"zod": packumentOf("zod", "4.6.5", "4.6.6", "4.6.7")}}, "newest"},

		// the grammar
		{"an anchor", strings.Replace(head, "settings:\n", "settings: &s\n", 1), nil, "a YAML anchor or tag"},
		{"an alias", strings.Replace(head, "settings:\n  autoInstallPeers: true\n  excludeLinksFromLockfile: false\n", "settings: &s\n  autoInstallPeers: true\n  excludeLinksFromLockfile: false\n\nother: *s\n", 1), nil, "a YAML"},
		{"an alias to nothing", strings.Replace(head, "settings:\n", "other: *nothing\nsettings:\n", 1), nil, "a YAML alias"},
		{"the merge key", with("    <<: {requiresBuild: true}\n"), nil, "<<"},
		{"an explicit tag", strings.Replace(head, "autoInstallPeers: true", "autoInstallPeers: !!str true", 1), nil, "a YAML anchor or tag"},
		{"a duplicate key", with("    hasBin: true\n    hasBin: true\n"), nil, "duplicate key"},
		{"a duplicate package", strings.Replace(head, zodNew, zodNew+zodNew, 1), nil, "duplicate key"},
		{"a forbidden key", with("    constructor: x\n"), nil, "the key constructor"},
		{"a double-quoted scalar", with("    engines: {node: \">=18\"}\n"), nil, "not plain or single-quoted"},
		{"a block scalar", with("    hasBin: |\n      true\n"), nil, "not plain or single-quoted"},
		{"a second document", head + "\n---\nlockfileVersion: '9.0'\n", nil, "more than one YAML document"},
		{"an empty file", "", nil, "is empty"},
		{"not YAML", head + "\n\t: [", nil, "not YAML"},
		{"a mapping key that is not a scalar", strings.Replace(head, zodNew, "  ? [a, b]\n  : x\n"+zodNew, 1), nil, "a mapping key that is not a scalar"},
		{"non-ASCII text", with("    engines: {node: 'é'}\n"), nil, "non-ASCII text outside a deprecated message"},
		{"the lockfile version unquoted", strings.Replace(head, "'9.0'", "9.0", 1), nil, "is not 9.0"},

		// what an entry may carry
		{"requiresBuild", with("    requiresBuild: true\n"), nil, "requiresBuild"},
		{"a tarball beside the resolution", with("    tarball: https://evil.test/x.tgz\n"), nil, "tarball"},
		{"a key nobody thought of", with("    somethingNew: true\n"), nil, "somethingNew"},
		{"hasBin spelled yes", with("    hasBin: yes\n"), nil, "hasBin is not true"},
		{"a resolution with a tarball", strings.Replace(head, "{integrity: "+intE+"}", "{integrity: "+intE+", tarball: https://evil.test/x.tgz}", 1), nil, "exactly an integrity"},
		{"a resolution that is a git commit", strings.Replace(head, "{integrity: "+intE+"}", "{repo: https://evil.test/x.git, commit: abc}", 1), nil, "exactly an integrity"},
		{"a package key that is a URL", strings.Replace(head, "  zod@4.6.6:\n", "  'zod@https://evil.test/zod.tgz':\n", 1), nil, "is not <name>@<semver>"},
		{"a package key that is a github shorthand", strings.Replace(head, "  zod@4.6.6:\n", "  'zod@github:u/r':\n", 1), nil, "is not <name>@<semver>"},
		{"a new package nothing depends on", strings.Replace(head, zodNew, zodNew+"\n  extra@1.0.0:\n    resolution: {integrity: "+intD+"}\n", 1), &more, "nothing depends on it"},
		{"an override pointing at git", strings.Replace(head, "importers:", "overrides:\n  zod: git+https://evil.test/zod.git\n\nimporters:", 1), nil, "not a semver range"},
		{"a patched dependency gained", strings.Replace(head, "importers:", "patchedDependencies:\n  zod@4.6.6:\n    hash: abc\n    path: patches/zod.patch\n\nimporters:", 1), nil, "patchedDependencies gains"},
		{"an unknown top-level key", strings.Replace(head, "importers:", "pnpmfile: ./hook.js\n\nimporters:", 1), nil, "pnpmfile"},
		{"an importer dependency on a workspace path", strings.Replace(head, "specifier: ^4.4.3", "specifier: workspace:*", 1), nil, "not a semver range"},
		{"a peer suffix bumped", strings.Replace(head, "tool@1.0.0: {}", "tool@1.0.0(zod@4.6.6): {}", 1), &more, ""},
		{"a peer suffix naming a URL", strings.Replace(head, "tool@1.0.0: {}", "tool@1.0.0(zod@https://evil.test/z.tgz): {}", 1), &more, "is not a semver version"},
	})
}

func TestManifestsAreReadInsideTheirDirectory(t *testing.T) {
	base := npmLock(3, fooEntry("1.0.0", intA, nil), nil)
	head := npmLock(3, fooEntry("1.0.1", intE, nil), nil)
	reg := npmRegistry()
	name := "manifests/foo@1.0.1.json"

	var out bytes.Buffer
	if code := runMain(t, &out, "package-lock.json", base, head, reg, nil); code != 0 {
		t.Fatalf("the control passes: %d %q", code, out.String())
	}
	// The same manifest, as a symbolic link to a file outside the directory.
	out.Reset()
	code := runMain(t, &out, "package-lock.json", base, head, reg, func(dir string) {
		outside := filepath.Join(dir, "outside.json")
		if err := os.Rename(filepath.Join(dir, name), outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
	})
	if line := strings.TrimSpace(out.String()); code != 1 || line != "FAIL: no recorded manifest of foo 1.0.1" {
		t.Errorf("a manifest that is a link out of the directory: %d %q", code, line)
	}
	// And a packument.
	out.Reset()
	code = runMain(t, &out, "package-lock.json", base, head, reg, func(dir string) {
		outside := filepath.Join(dir, "outside.packument.json")
		packument := filepath.Join(dir, "manifests/foo.packument.json")
		if err := os.Rename(packument, outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, packument); err != nil {
			t.Skipf("no symbolic links here: %v", err)
		}
	})
	if line := strings.TrimSpace(out.String()); code != 1 || line != "FAIL: no recorded packument of foo" {
		t.Errorf("a packument that is a link out of the directory: %d %q", code, line)
	}
}

func TestLockfileReadRefusals(t *testing.T) {
	base := npmLock(3, fooEntry("1.0.0", intA, nil), nil)
	head := npmLock(3, fooEntry("1.0.1", intE, nil), nil)
	good := npmRegistry()
	insert := func(text, extra string) string {
		return strings.Replace(text, `"name": "demo"`, `"name": "demo`+extra+`"`, 1)
	}

	original := maxLockfileBytes
	t.Cleanup(func() { maxLockfileBytes = original })

	if code, line := verdict(t, "package-lock.json", base, head, good); code != 0 {
		t.Fatalf("the control passes: %d %q", code, line)
	}
	for _, c := range []struct {
		label      string
		base, head string
		want       string
	}{
		{"a U+0085 line break in the head", base, insert(head, string(rune(0x85))), "a line break that only some YAML readers recognise"},
		{"a U+2028 line break in the head", base, insert(head, string(rune(0x2028))), "a line break that only some YAML readers recognise"},
		{"a U+2029 line break in the head", base, insert(head, string(rune(0x2029))), "a line break that only some YAML readers recognise"},
		{"a U+2028 line break in the base", insert(base, string(rune(0x2028))), head, "a line break that only some YAML readers recognise"},
		{"a conflict marker <<<<<<<", base, insert(head, "<<<<<<<"), "a merge conflict marker, which pnpm reads a second way"},
		{"a conflict marker =======", base, insert(head, "======="), "a merge conflict marker, which pnpm reads a second way"},
		{"a conflict marker >>>>>>>", base, insert(head, ">>>>>>>"), "a merge conflict marker, which pnpm reads a second way"},
		{"a conflict marker |||||||", base, insert(head, "|||||||"), "a merge conflict marker, which pnpm reads a second way"},
		{"a conflict marker in the base", insert(base, "======="), head, "a merge conflict marker, which pnpm reads a second way"},
		{"text that is not UTF-8", base, insert(head, "\xff"), "a lockfile that is not UTF-8"},
	} {
		if code, line := verdict(t, "package-lock.json", c.base, c.head, good); code != 1 || line != "FAIL: "+c.want {
			t.Errorf("%s: %d %q, want %q", c.label, code, line, c.want)
		}
	}

	maxLockfileBytes = int64(len(head))
	if code, line := verdict(t, "package-lock.json", base, head, good); code != 0 {
		t.Errorf("a lockfile of exactly the limit is read: %d %q", code, line)
	}
	maxLockfileBytes = int64(len(head)) - 1
	if code, line := verdict(t, "package-lock.json", base, head, good); code != 1 || line != "FAIL: a lockfile larger than the limit" {
		t.Errorf("a lockfile one byte over the limit: %d %q", code, line)
	}
	maxLockfileBytes = int64(len(base)) - 1
	if code, line := verdict(t, "package-lock.json", base, head, good); code != 1 || line != "FAIL: a lockfile larger than the limit" {
		t.Errorf("a base over the limit: %d %q", code, line)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestAVerdictThatCannotBeWrittenIsNotAPass(t *testing.T) {
	base := npmLock(3, fooEntry("1.0.0", intA, nil), nil)
	head := npmLock(3, fooEntry("1.0.1", intE, nil), nil)
	if code := runMain(t, failingWriter{}, "package-lock.json", base, head, npmRegistry(), nil); code == 0 {
		t.Errorf("a PASS that could not be written exits %d", code)
	}
	if code := runMain(t, failingWriter{}, "package-lock.json", base, base+"x", npmRegistry(), nil); code != 1 {
		t.Errorf("a refusal that could not be written exits %d", code)
	}
	if code := Main(nil, failingWriter{}); code != 1 {
		t.Errorf("a usage line that could not be written exits %d", code)
	}
}

// linkOut moves the file called name in dir to a directory of its own and leaves a symbolic link to
// it in its place.
func linkOut(t *testing.T, dir, name string) {
	t.Helper()
	outside := filepath.Join(t.TempDir(), name)
	if err := os.Rename(filepath.Join(dir, name), outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
}

func TestFilesNamedOnTheCommandLineAreReadInsideTheirDirectory(t *testing.T) {
	base := npmLock(3, fooEntry("1.0.0", intA, nil), nil)
	head := npmLock(3, fooEntry("1.0.1", intE, nil), nil)
	var out bytes.Buffer
	if code := runMain(t, &out, "package-lock.json", base, head, npmRegistry(), nil); code != 0 {
		t.Fatalf("the control passes: %d %q", code, out.String())
	}
	for _, name := range []string{"base", "head"} {
		out.Reset()
		code := runMain(t, &out, "package-lock.json", base, head, npmRegistry(), func(dir string) { linkOut(t, dir, name) })
		if line := strings.TrimSpace(out.String()); code != 1 || !strings.HasPrefix(line, "FAIL: ") {
			t.Errorf("the %s file as a link out of its directory: %d %q", name, code, line)
		}
	}
}
