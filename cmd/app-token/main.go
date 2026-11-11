// Command app-token mints a GitHub App installation token the way actions/create-github-app-token
// does, and revokes it again. `app-token mint` prints `::add-mask::<token>` first, then writes
// `token=`, `installation-id=` and `app-slug=` to $GITHUB_OUTPUT; `app-token revoke` deletes the
// token it is given.
package main

import (
	"os"

	"github.com/nicodarge/go-tools/internal/apptoken"
)

func main() {
	os.Exit(apptoken.Main(os.Args[1:], apptoken.Env{
		Getenv: os.Getenv,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}
