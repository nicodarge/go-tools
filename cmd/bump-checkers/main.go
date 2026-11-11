// Command bump-checkers holds the structured checks a merge guard runs on a pull request that
// changes a lockfile or a tool checksum: `lockfile` and `checksum`. Each prints PASS and exits 0, or
// prints `FAIL: <reason>` and exits 1.
package main

import (
	"fmt"
	"os"

	"github.com/nicodarge/go-tools/internal/checksumcheck"
	"github.com/nicodarge/go-tools/internal/lockfilecheck"
)

// extra holds subcommands of the equivalence build only (go build -tags equiv).
var extra = map[string]func(args []string) int{}

func main() {
	if len(os.Args) >= 2 {
		if run, ok := extra[os.Args[1]]; ok {
			os.Exit(run(os.Args[2:]))
		}
		switch os.Args[1] {
		case "lockfile":
			os.Exit(lockfilecheck.Main(os.Args[2:], os.Stdout))
		case "checksum":
			os.Exit(checksumcheck.Main(os.Args[2:], os.Stdout))
		}
	}
	fmt.Println("FAIL: usage: bump-checkers lockfile|checksum <arguments>")
	os.Exit(1)
}
