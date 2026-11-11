package lockfilecheck

import (
	"strings"

	"github.com/nicodarge/go-tools/internal/core"
)

var installScripts = []string{"preinstall", "install", "postinstall"}

func manifestMap(manifest *value, key, what string) *core.OMap {
	v := orEmpty(mget(manifest, key))
	if !isMap(v) {
		reject(what + ": the manifest " + key + " is not a map of strings")
	}
	for _, k := range v.M.Keys {
		if !isStr(v.M.Vals[k]) {
			reject(what + ": the manifest " + key + " is not a map of strings")
		}
	}
	return v.M
}

func manifestIntegrity(manifest *value, integrity *value, what string) {
	dist := mget(manifest, "dist")
	if !isMap(dist) || !isIntegrity(dist.M.Get("integrity")) {
		reject(what + ": the manifest has no integrity")
	}
	if !pyEq(dist.M.Get("integrity"), integrity) {
		reject(what + " integrity is not the one the registry publishes")
	}
}

func manifestNoInstall(manifest *value, what string) {
	scripts := orEmpty(mget(manifest, "scripts"))
	if !isMap(scripts) {
		reject(what + ": the manifest scripts are not a mapping")
	}
	found := false
	for _, key := range installScripts {
		if scripts.M.Has(key) {
			found = true
		}
	}
	if truthy(mget(manifest, "hasInstallScript")) || truthy(mget(manifest, "gypfile")) || found {
		reject(what + " declares an install script")
	}
}

func stripDot(path string) string { return strings.TrimPrefix(path, "./") }

func manifestBin(manifest *value, name, what string) *core.OMap {
	v := orEmpty(mget(manifest, "bin"))
	if isStr(v) {
		m := core.NewMap()
		m.Set(name[strings.LastIndex(name, "/")+1:], v)
		v = core.MapValue(m)
	}
	if !isMap(v) {
		reject(what + ": the manifest bin is not a map of strings")
	}
	out := core.NewMap()
	for _, k := range v.M.Keys {
		if !isStr(v.M.Vals[k]) {
			reject(what + ": the manifest bin is not a map of strings")
		}
		out.Set(k, str(stripDot(v.M.Vals[k].S)))
	}
	return out
}

func manifestHasBin(manifest *value, name, what string) bool {
	directories := mget(manifest, "directories")
	return manifestBin(manifest, name, what).Len() > 0 ||
		(isMap(directories) && truthy(directories.M.Get("bin")))
}
