// Command version prints the version of this module's release.
package main

import (
	"fmt"

	"github.com/nicodarge/go-tools/internal/buildinfo"
)

func main() {
	fmt.Println(buildinfo.String())
}
