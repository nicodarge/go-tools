# go-tools

Go command-line tools for CI and merge guards, released as checksum-pinned static binaries.

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
| `.pre-commit-hooks.yaml` | The hooks other repositories use through `repo: https://github.com/nicodarge/go-tools` |

`cmd/version` prints the version of the release it was built from.

## bump-checkers

`bump-checkers <subcommand> <arguments>` holds the two structured checks a merge guard runs on a pull request that changes a dependency lockfile or a tool-pin file. Each reads the complete base and head files, not the diff, and refuses anything it cannot positively classify as a version bump.

```text
bump-checkers lockfile [--manifests DIR] [--before TIME] <path> <base file> <head file>
bump-checkers checksum [--urls FILE] <mapping file> <path> <base file> <head file>
```

| Subcommand | Judges | Reads from the network |
| --- | --- | --- |
| `lockfile` | `package-lock.json`, `npm-shrinkwrap.json` (`lockfileVersion` 2 and 3) and `pnpm-lock.yaml`: every new or changed entry must be what the npm registry publishes for that name and version, inside its range, not lower than the base, and the newest version published by `TIME` | `registry.npmjs.org`, 300 requests and 90 seconds in all, no redirect followed |
| `checksum` | A Dockerfile (`ARG <TOOL>_VERSION=` and `ARG <TOOL>_SHA256_<ARCH>=` lines) or a dotenv-style tool-pin file (the same lines with no `ARG` prefix): exactly one tool's version moves, up, and every changed digest is the one upstream publishes for it | `api.github.com` and `github.com` (their asset host after one redirect), 60 seconds in all |

Arguments:

- `<path>` is the repository path of the file, which decides how it is read. `lockfile` takes it from the file name (`package-lock.json`, `npm-shrinkwrap.json`, `pnpm-lock.yaml`). `checksum` accepts the last segment `Dockerfile` or a name ending in `.env` with something before it, and refuses any other, a bare `.env` included.
- `--before TIME` is the UTC instant (`YYYY-MM-DDThh:mm:ss`, optionally with fractional seconds, then `Z`) that a new version is held against: it must be the newest version the registry had published by `TIME` plus 300 seconds of clock skew. Without it, a change that brings in a new version is refused.
- `<mapping file>` is the repository's tool table: a Python file of `Tool(prefix, repo, tag, assets[, checksums])` calls whose arguments are read as literal data and never executed. **Read it from the base ref, never from the head of the change being judged**: the head is what the check distrusts.
- Every file named on the command line is opened through its directory, so a symbolic link that leaves that directory fails closed, as does a recorded manifest in `DIR` that does.
- `--manifests DIR` and `--urls FILE` replace the network with recorded answers (`DIR` holds `<name with / as %2F>@<version>.json` manifests and `<name with / as %2F>.packument.json` packuments; `FILE` is a JSON object of `{url: {"status": n, "body": text}}`). They exist for tests: **never feed them data a pull request controls**, or the check judges the change against the answers the change brought.

`checksum` reads a tool's `{ubuntu}` placeholder (in its tag, asset or checksums template) from the file's single `RUBY_UBUNTU_VERSION` pin line, and refuses a tool that needs it when that line is missing or repeated. A new version is a downgrade when it is older by number, or a prerelease of the version it replaces, whatever the spelling (`2.0.0` to `2.0.0-rc.1`, `2.0.0-1`, `2.0.0rc1`, `2.0.0.rc1`, `2.0.0_rc1`); build metadata after `+` is ignored, and tokens that are not dotted numbers are compared run by run.

Exit codes: `PASS` and exit `0` for a change that is only a version bump; `FAIL: <reason>` and exit `1` for everything else, including a file that does not parse, a registry or a release that cannot be read, wrong arguments (`FAIL: usage: ...`) and an unexpected error (`FAIL: internal error (<name>)`). **A caller accepts a change only on exit code `0` and a first output line of exactly `PASS`**; anything else, a crash or a missing binary included, is a refusal.

A packument is read while it arrives and only its `name`, `time` and `versions` are kept, so the memory used does not grow with the size of a package's history. A packument over 64 MiB, a single member of it over 8 MiB of text, or a manifest over 8 MiB is refused; every answer is closed whether it was read or refused.

`go build -tags equiv ./cmd/bump-checkers` builds a variant with extra subcommands for driving the checks from an external test suite. It is never released.

## golangci-guard

`golangci-guard` keeps golangci-lint at zero tolerance. It replaces regular-expression hooks, which cannot tell a weakened setting from a strong one: the configuration is parsed and every key and value is checked against an allowlist, and anything not on it is refused.

```text
golangci-guard config [--require LINTER,...] [DIR|FILE ...]
golangci-guard scan-go FILE...
```

Both print `PASS` and exit `0`, or print one `FAIL: <path>: <reason>` line per refusal and exit `1`. A caller accepts only exit code `0`; a crash or a missing binary is a refusal.

### `config`

Each argument is a directory (the default is `.`) or a `.golangci.*` file in one. The directory must hold exactly one entry whose name starts with `.golangci.`, and it must be a regular file called `.golangci.yaml` or `.golangci.yml`: golangci-lint reads the first of `.yml`, `.yaml`, `.toml` and `.json` it finds, so a second file, a TOML or JSON file, a symbolic link or a directory with that prefix is refused. A directory with no configuration is refused too. Any other entry whose name starts with `.golangci.` makes the directory fail on purpose, an editor leftover such as `.golangci.yaml~` or `.golangci.yaml.orig` included: delete it.

The YAML is read through its node tree. These are refused wherever they appear: an anchor, an alias, an explicit tag, a merge key (`<<`), a key that is not a plain string (a sequence or mapping key, a number, a null), a duplicate key, a second document, a file that is empty, over 1 MiB, not UTF-8 or nested over 32 levels. Key names are exact and lower case. The parsed structure must then match this allowlist exactly:

| Path | Rule |
| --- | --- |
| top level | keys only `version`, `run`, `linters`, `issues`, `formatters`; `version`, `run`, `linters` and `issues` required |
| `version` | the string `"2"` |
| `run` | keys only `timeout`, `tests`, `build-tags`; `tests: true` required; `timeout`, if present, a positive duration string such as `5m`; `build-tags`, if present, a non-empty list of distinct names matching `[a-z][a-z0-9_-]*` |
| `linters` | keys only `enable`, `settings`, `exclusions`; so `default`, `disable` and anything else are refused |
| `linters.enable` | required; a list of distinct names matching `[a-z][a-z0-9_-]*`; must contain the default required linters (`bodyclose`, `errorlint`, `gosec`, `misspell`, `nolintlint`, `revive`, `unconvert`, `unparam`) and every `--require` linter; other linters may be added |
| `linters.exclusions` | required and exactly `{generated: disable}` |
| `linters.settings` | optional; keys only `gosec`, `nolintlint`, `govet` |
| `linters.settings.gosec` | exactly `{config: {global: {nosec: true}}}`, which makes gosec ignore its suppression comments; required whenever `gosec` is enabled; `includes`, `excludes`, `severity`, `confidence` and every other key are refused |
| `linters.settings.nolintlint` | keys only `require-explanation` and `require-specific` (each `true` if present) and `allow-unused` (`false` if present) |
| `linters.settings.govet` | keys only `enable` (a list of analyzer names) and `enable-all` (`true` if present); `disable` and `disable-all` are refused |
| `issues` | required and exactly `max-issues-per-linter: 0` and `max-same-issues: 0`, as the integer `0` |
| `formatters` | optional; keys only `enable` (a list of names); `exclusions`, `settings` and anything else are refused |

Booleans are the literals `true` and `false`; `True`, `yes` and a quoted string do not count. Settings of any other linter (`staticcheck.checks`, `revive.rules`, ...) are refused: to allow one, add it to the allowlist in `internal/golangciguard/config.go` with a test.

`--require` adds linters to the default required set (it may be given several times, each with a comma-separated list); it never removes one, so a configuration missing a default linter is refused whatever `--require` says.

Build constraints: golangci-lint lints one environment per run (the runner's architecture, cgo on or off, its own toolchain's release tags), so the guard also walks the Go files under the directory and accepts a file only when every run lints it. It is an allowlist:

- a `//go:build` or `// +build` line may name only `linux`, `unix`, `gc` and the entries of `run.build-tags`, none of them under an odd number of negations. `cgo`, any other GOOS or GOARCH (`amd64` and `arm64` included), `go1.N`, `amd64.v1`, `goexperiment.*`, `ignore`, `tools` and any unknown tag are refused, as are `!linux`, `!unix`, `!gc` and a negated `run.build-tags` entry (`//go:build !integration` while `build-tags: [integration]`), which setting the tag would take out of the lint. More than one `//go:build` line is refused;
- a file name may carry no `_GOOS`, `_GOARCH` or `_GOOS_GOARCH` suffix other than `_linux.go` and `_linux_test.go`, judged by the toolchain's own list of systems and architectures;
- a file that imports `"C"`, or an import path with a `testdata` element, is refused;
- a nested module (a subdirectory with a go.mod that is a regular file) is not entered by a run from the directory above, so it needs a `.golangci.yaml` of its own, and gets this whole check; the exception is a module that holds no Go file, which has nothing to lint. A go.mod that is not a regular file is refused;
- a Go file under a directory starting with `_` or `.`, or named `vendor`, is refused: the go tool skips those directories, yet they stay importable. `testdata` directories are not walked, and `.git` is ignored;
- a symbolic link to a directory is refused, because the walk does not follow it; a `.go` entry that is not a regular file is refused; files starting with `_` or `.` are ignored, as the go tool ignores them.

A file whose header cannot be parsed is refused. A refusal names the path and the offending term, relative to the directory the guard was given.

Consumers lose the following: generator scripts (`//go:build ignore`) and `tools` files go to a nested module with its own configuration or out of the Go sources; `_windows`, `_darwin` and architecture-specific files, cgo and `go1.N` constraints are not accepted anywhere under the directory; a tag a file needs is listed in `run.build-tags`, never negated.

The guard judges the configuration file and the Go sources only. The command line that runs golangci-lint is outside its reach and must stay `golangci-lint run`, with no flag that weakens it (`--issues-exit-code`, `--new-from-rev`, `--disable`, `--config`, `--no-config`, `--build-tags`).

### `scan-go`

Tokenizes each Go file and refuses any line or block comment that contains `nolint`, `nosec`, `revive:` or `exhaustive:`, in any case and with any spacing between the comment marker and the word. The list covers the suppression directives of the linters golangci-lint honours (`nolint`, `nosec`, `revive`, `exhaustive`). A comment that merely mentions one of the words, such as `nolintlint` in prose, is refused on purpose: the guard does not try to tell a directive from a sentence. String, raw string and rune literals are not comments and are ignored. A file that cannot be read, is not a regular file, is over 32 MiB or cannot be tokenized (an unterminated comment or string, an illegal character) is refused. A refusal names the real line of the file, whatever a `//line` directive says.

### Using the hooks

```yaml
repos:
  - repo: https://github.com/nicodarge/go-tools
    rev: vX.Y.Z
    hooks:
      - id: golangci-config-guard
      - id: go-no-suppression
```

`rev` is a release tag. `golangci-config-guard` runs on every commit and checks the configuration in the repository root (`args: ["--require", "a,b,c"]` adds required linters; `args: [dir]` checks another directory). `go-no-suppression` runs on the Go files being committed. Both are declared for the `pre-commit` and `manual` stages, so a CI job running the `manual` stage re-runs them. The hooks use `language: golang`: pre-commit builds the binary from the tagged source, so a Go toolchain that satisfies `go.mod` must be available.

This repository runs the same two hooks from its own working tree (`go run ./cmd/golangci-guard`), so a change to the guard is checked by the guard.

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

GoReleaser creates the release as a draft, uploads the assets, then publishes it, so a release without its assets is never public. The workflow uses the job's `GITHUB_TOKEN` only and needs no secret. A re-run repairs a tag whose release is missing or still a draft: GoReleaser reuses the draft and replaces its assets.

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

Install the commit hooks once: `pre-commit install`. They run `gofmt -s -w`, actionlint, yamllint, shellcheck and markdownlint, `golangci-guard` on the golangci-lint configuration and on Go comments, plus whitespace and secret guards. Tests, `go vet` and golangci-lint run in CI only.
