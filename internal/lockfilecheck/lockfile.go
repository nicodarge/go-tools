// Package lockfilecheck decides whether a lockfile changed only in ways a dependency bump may
// change it. A change has to pass every layer: shape, immutability, the registry, ranges, no
// lowering, nothing old brought in.
package lockfilecheck

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicodarge/go-tools/internal/core"
)

const usage = "FAIL: usage: bump-checkers lockfile [--manifests DIR] [--before TIME] [--min-release-age AGE] <path> <base file> <head file>"

// maxLockfileBytes is the size of the largest lockfile read; a variable so a test can lower it.
var maxLockfileBytes int64 = maxBytes

// equivHook is set only by the equivalence build (go build -tags equiv); see equiv.go.
var equivHook func()

// say writes a line of the verdict and returns the error if it could not be written.
func say(out io.Writer, line string) error {
	_, err := fmt.Fprintln(out, line)
	return err
}

// publishedBefore is {version: deprecated} for every version published by `before`, in the order
// the packument lists them. A packument with no `time`, or a version with no time in it, cannot be
// placed in time, and refuses: a version left out would make an older one the newest.
func publishedBefore(p *core.Packument, name string, before time.Time) ([]string, map[string]bool) {
	if !core.IsMap(p.Time) || !p.HasVers {
		reject("the packument of " + q(name) + " has no publication times")
	}
	var order []string
	out := map[string]bool{}
	for _, e := range p.Versions {
		stamp, ok := p.Time.M.Vals[e.Name]
		if !ok {
			reject("the packument of " + q(name) + " gives no time for " + q(e.Name))
		}
		if !parseStamp(stamp, "the packument of "+q(name)).After(before) {
			order = append(order, e.Name)
			out[e.Name] = e.IsObject && e.Deprecated
		}
	}
	return order, out
}

// newest is the newest version of the pool inside every range, a deprecated one only if nothing else is.
func newest(order []string, pool map[string]bool, ranges []*value) (string, bool) {
	type item struct {
		deprecated bool
		key        version
		text       string
	}
	var inside []item
	for _, v := range order {
		var key version
		ok := func() (ok bool) {
			defer func() {
				if r := recover(); r != nil {
					if _, is := r.(core.Rejection); is {
						ok = false
						return
					}
					panic(r)
				}
			}()
			key = parseVersion(v)
			return true
		}()
		if !ok {
			continue
		}
		all := true
		for _, spec := range ranges {
			if !satisfies(v, spec) {
				all = false
				break
			}
		}
		if all {
			inside = append(inside, item{pool[v], key, v})
		}
	}
	kept := inside[:0:0]
	for _, it := range inside {
		if !it.deprecated {
			kept = append(kept, it)
		}
	}
	if len(kept) == 0 {
		kept = inside
	}
	if len(kept) == 0 {
		return "", false
	}
	best := kept[0]
	for _, it := range kept[1:] {
		if cmpVersion(it.key, best.key) > 0 {
			best = it
		}
	}
	return best.text, true
}

// newestAllowed is what a new entry for `name` may resolve to: the newest version published by
// `before`, or the newest one the base lockfile already holds, inside every range that asks for it.
func newestAllowed(name string, ranges []*value, p *core.Packument, before time.Time, held []string) map[string]bool {
	order, pool := publishedBefore(p, name, before)
	allowed := map[string]bool{}
	if v, ok := newest(order, pool, ranges); ok {
		allowed[v] = true
	}
	heldPool := map[string]bool{}
	for _, h := range held {
		heldPool[h] = false
	}
	if v, ok := newest(held, heldPool, ranges); ok {
		allowed[v] = true
	}
	return allowed
}

// settle is rule 6: nothing old is brought in.
func settle(sources []func() []claim, registry Registry, before *time.Time) {
	var claims []claim
	for _, s := range sources {
		claims = append(claims, s()...)
	}
	if len(claims) == 0 {
		return
	}
	if before == nil {
		reject("no time to hold the new versions against")
	}
	names := map[string]bool{}
	var packs []pair
	for _, c := range claims {
		if !names[c.name] {
			names[c.name] = true
			packs = append(packs, pair{name: c.name, isPack: true})
		}
	}
	registry.Prefetch(packs)
	for _, c := range claims {
		allowed := newestAllowed(c.name, c.ranges, registry.Packument(c.name), *before, c.held)
		if !allowed[c.version] {
			reject(c.what + ": " + q(c.version) + " is neither the newest " + q(c.name) + " inside its range published by " +
				"the time of the decision nor the newest the base already holds")
		}
	}
}

// verify runs every deferred comparison against the manifest of its name@version.
func verify(todo []todoItem, registry Registry) {
	seen := map[pair]bool{}
	var pairs []pair
	for _, t := range todo {
		p := pair{name: t.name, version: t.version}
		if !seen[p] {
			seen[p] = true
			pairs = append(pairs, p)
		}
	}
	registry.Prefetch(pairs)
	for _, t := range todo {
		t.compare(registry.Get(t.name, t.version))
	}
}

// Check judges one lockfile pair. `before` is the UTC time (YYYY-MM-DDTHH:MM:SSZ) the new versions
// are held against, nil when none was given; `minReleaseAge` (see parseDuration) is how much older
// than that a version must be to count as the newest, nil for none.
func Check(path string, baseText, headText []byte, registry Registry, before, minReleaseAge *string) {
	name := lastSegment(path)
	var todo []todoItem
	var claims []func() []claim
	var limit *time.Time
	var age time.Duration
	if minReleaseAge != nil {
		age = parseDuration(*minReleaseAge, "the minimum release age")
	}
	if before != nil {
		t := parseStamp(*before, "the time of the decision").Add(clockSkew - age)
		limit = &t
	}
	switch name {
	case "package-lock.json", "npm-shrinkwrap.json":
		checkNPM(baseText, headText, &todo, &claims)
	case "pnpm-lock.yaml":
		checkPNPM(string(baseText), string(headText), &todo, &claims)
	default:
		reject(q(name) + " is not a lockfile this knows")
	}
	verify(todo, registry)
	settle(claims, registry, limit)
}

var lineBreaks = []string{"\u0085", "\u2028", "\u2029"}
var conflictMarkers = []string{"<<<<<<<", "=======", ">>>>>>>", "|||||||"}

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
func readText(path string) []byte {
	f, err := core.OpenFile(path)
	if err != nil {
		openError(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxLockfileBytes+1))
	if err != nil {
		openError(err)
	}
	if int64(len(data)) > maxLockfileBytes {
		reject("a lockfile larger than the limit")
	}
	if !utf8.Valid(data) {
		reject("a lockfile that is not UTF-8")
	}
	text := string(data)
	for _, b := range lineBreaks {
		if strings.Contains(text, b) {
			reject("a line break that only some YAML readers recognise")
		}
	}
	for _, m := range conflictMarkers {
		if strings.Contains(text, m) {
			reject("a merge conflict marker, which pnpm reads a second way")
		}
	}
	return data
}

// Main runs the lockfile check on args (after the subcommand) and writes its verdict to out: PASS
// and 0, or `FAIL: <reason>` and 1.
func Main(args []string, out io.Writer) int {
	options := map[string]string{}
	for len(args) >= 2 && (args[0] == "--manifests" || args[0] == "--before" || args[0] == "--min-release-age") {
		if _, dup := options[args[0]]; dup {
			break
		}
		options[args[0]], args = args[1], args[2:]
	}
	if len(args) != 3 {
		_ = say(out, usage)
		return 1
	}
	line := core.Guard(func() {
		if equivHook != nil {
			equivHook()
		}
		base := readText(args[1])
		head := readText(args[2])
		var registry Registry = NewHTTPRegistry()
		if dir, ok := options["--manifests"]; ok {
			registry = NewDirRegistry(dir)
		}
		var before *string
		if b, ok := options["--before"]; ok {
			before = &b
		}
		var minReleaseAge *string
		if a, ok := options["--min-release-age"]; ok {
			minReleaseAge = &a
		}
		Check(args[0], base, head, registry, before, minReleaseAge)
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
