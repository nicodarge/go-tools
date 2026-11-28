// Command netbox-cable-sync derives physical cables from a UniFi Network controller (LLDP
// neighbours and wired clients) and keeps them in NetBox. It prints a plan and writes only with
// --apply.
package main

import (
	"os"

	"github.com/nicodarge/go-tools/internal/netboxcablesync"
)

func main() {
	os.Exit(netboxcablesync.Main(os.Args[1:], netboxcablesync.Env{
		Getenv: os.Getenv,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}
