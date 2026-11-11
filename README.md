# go-tools

Go command-line tools for the Groniko homelab CI and guards, released as checksum-pinned static binaries.

No tool here needs Node.js, Python or any runtime: each is one static Go binary (`CGO_ENABLED=0`) for `linux/amd64` and `linux/arm64`.

## Layout

| Path | Content |
| --- | --- |
| `cmd/<tool>/` | One `main` package per tool; the directory name is the binary name |
| `internal/` | Packages shared by the tools; not importable from other modules |
| `tools/<name>/` | One Go module per CI tool (`golangci-lint`, `goreleaser`, `svu`), pinned in its `go.mod` and verified by its `go.sum` |
| `scripts/install-tools.sh` | Builds the pinned CI tools from `tools/` |
| `.goreleaser.yaml` | Release builds and the `SHA256SUMS` file |
| `.golangci.yaml` | Linter configuration |

`cmd/version` prints the version of the release it was built from.

## Adding a tool

1. Create `cmd/<tool>/main.go` (package `main`) and put reusable code under `internal/`.
2. Add a build to `.goreleaser.yaml`: copy the `version` entry and change `id`, `main` and `binary`. The `release-config` job of `ci.yaml` fails when a `cmd/` directory has no build.
3. Write tests next to the code; CI runs `go test -race ./...`.
4. Open a pull request whose title is a conventional commit: `feat: <tool> ...` releases a minor version.

## Releases

A push to `production` runs `release.yaml`:

1. `svu next` derives the version from the conventional commits since the last tag: `fix:` releases a patch, `feat:` a minor, `feat!:` or a `BREAKING CHANGE:` footer a major. Other types (`chore:`, `docs:`, `ci:`, `test:`, `refactor:`) release nothing.
2. The job tags the commit `vX.Y.Z` and pushes the tag.
3. GoReleaser builds every `cmd/` for `linux/amd64` and `linux/arm64` and publishes the binaries as release assets, named `<tool>_linux_<arch>`, with a `SHA256SUMS` file.

GoReleaser creates the release as a draft, and the last step publishes it, so a release without its assets is never public. The workflow uses the job's `GITHUB_TOKEN` only and needs no secret. A re-run repairs a tag whose release is missing or still a draft.

Never create or move a tag by hand: version tags are immutable, protected by a tag ruleset (update and deletion refused on `v*`).

## Consuming a release

Pin the version and the SHA-256 digest of the binary; both come from the release page.

```bash
VERSION=vX.Y.Z
SHA256=<digest of <tool>_linux_amd64 from SHA256SUMS of that release>
curl -fsSLo /usr/local/bin/<tool> "https://github.com/nicodarge/go-tools/releases/download/${VERSION}/<tool>_linux_amd64"
echo "${SHA256}  /usr/local/bin/<tool>" | sha256sum -c -
chmod +x /usr/local/bin/<tool>
```

On arm64, use `<tool>_linux_arm64` and its digest. `SHA256SUMS` is published in the same release as the binary, so it catches a corrupted or substituted download, not a tampered release: take the digest into your own repository by hand or through a reviewed pull request, never fetch it at run time.

## CI

Every workflow runs on GitHub-hosted `ubuntu-latest` runners and holds no secret.

| Workflow | Runs | What |
| --- | --- | --- |
| `ci.yaml` | pull requests | `go vet`, `go test -race ./...`, golangci-lint, a `CGO_ENABLED=0` build of every `cmd/` for `linux/amd64` and `linux/arm64`, and `goreleaser check` |
| `lint.yaml` | pull requests | The pre-commit linters and guards (`manual` stage) over the whole tree, as a backstop for the commit-stage hooks; the fixers stay commit-only |
| `release.yaml` | push to `production` | The release described above |

The CI tools (`golangci-lint`, `goreleaser`, `svu`) are Go modules in `tools/`: `go.sum` pins their source, and Renovate bumps them like any Go dependency. There is one module per tool because their dependency graphs conflict in a single `go.mod`.

Renovate (`renovate.json`) bumps Go modules, GitHub Actions and the pre-commit hooks (its `pre-commit` manager is enabled explicitly) every Monday and merges patch and minor updates once CI is green. A major, a `0.x` or a prerelease update stays open for a person. Actions are pinned by commit SHA.

## Development

Install the commit hooks once: `pre-commit install`. They run `gofmt -s -w`, actionlint, yamllint, shellcheck and markdownlint, plus whitespace and secret guards. Tests, `go vet` and golangci-lint run in CI only.
