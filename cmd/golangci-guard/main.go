// Command golangci-guard refuses a golangci-lint configuration that is not on its allowlist, and Go
// sources that carry a lint suppression directive. It prints PASS and exits 0, or prints
// `FAIL: <reason>` lines and exits 1.
package main

import (
	"os"

	"github.com/nicodarge/go-tools/internal/golangciguard"
)

func main() {
	os.Exit(golangciguard.Main(os.Args[1:], os.Stdout))
}
