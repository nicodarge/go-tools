// Package buildinfo reports the version a binary was built from.
package buildinfo

import "runtime/debug"

// Version is set at release time with -ldflags "-X github.com/nicodarge/go-tools/internal/buildinfo.Version=vX.Y.Z".
var Version = ""

// String returns the release version, else the module version the Go toolchain recorded, else "dev".
func String() string {
	if Version != "" {
		return Version
	}
	return fromBuildInfo(debug.ReadBuildInfo())
}

func fromBuildInfo(info *debug.BuildInfo, ok bool) string {
	if ok && info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
