# CLAUDE.md

The README describes the layout, the release flow and how a consumer pins a binary; this file is what to keep true when changing it.

## Rules

- Public repository: no secret, no self-hosted runner label, no reference to internal hosts, addresses or credentials. Every job runs on `ubuntu-latest`.
- No Node.js and no other runtime in a tool: Go only, `CGO_ENABLED=0`, `linux/amd64` and `linux/arm64`. The `markdownlint` pre-commit hook is the one third-party Node tool, kept because it has no equivalent without a regression.
- A directory in `cmd/` needs a build in `.goreleaser.yaml` (the `release-config` job refuses otherwise); a new `cmd/` binary is a `feat:`.
- Releases are `release.yaml`'s: never create, move or delete a `v*` tag by hand, and never edit a version. The pull request title (squash subject) is the conventional commit `svu` reads: `fix:` patch, `feat:` minor, `feat!:` major; `chore:`, `docs:`, `ci:`, `test:` and `refactor:` release nothing.
- CI tools live in `tools/<name>/` as one Go module each, installed by `scripts/install-tools.sh`. Do not merge them into one `go.mod`: their dependency graphs conflict. Never download a CI tool's binary in a workflow; add it as a module.
- Pin every GitHub Action by commit SHA with the tag in a trailing comment.
- The golangci-lint config is `.golangci.yaml`. Every golangci-lint finding fails CI and is fixed at source: no `//nolint`, no exclusions.

## CI

- `ci.yaml` and `lint.yaml` run on pull requests; a new workflow that puts a check on a pull request needs no change elsewhere, as no merge workflow reads the list.
- Renovate merges green patch and minor updates itself. There is no `pr-review`, `bump-review` or `auto-merge` workflow here: the shared workflows live in a private repository a public one cannot call.
- No AI review runs in this repository's CI: run the `pr-reviewer` subagent locally on the pushed branch before `gh pr create`.
