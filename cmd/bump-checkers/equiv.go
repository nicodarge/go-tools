//go:build equiv

package main

import (
	"os"

	"github.com/nicodarge/go-tools/internal/checksumcheck"
	"github.com/nicodarge/go-tools/internal/lockfilecheck"
)

func init() {
	extra["packument"] = func(args []string) int { return lockfilecheck.EquivPackument(args, os.Stdout) }
	extra["satisfies"] = func(args []string) int { return lockfilecheck.EquivSatisfies(args, os.Stdout) }
	for _, name := range []string{"parse-tools", "checksum-file", "release", "redirect", "urlopen-host", "fetch-digests"} {
		extra[name] = func(args []string) int {
			code, _ := checksumcheck.EquivCommand(name, args, os.Stdout)
			return code
		}
	}
}
