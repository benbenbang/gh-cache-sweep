# gh cache-sweep 🧹

Preview or delete GitHub Actions caches across an organization's repositories, a user's repositories, a team's repositories, or one repository. Written in Go using only the standard library; delegates authentication and cache deletion to [GitHub CLI](https://github.com/cli/cli).

The command is `gh cache-sweep`, not `gh cache` (which is already a built-in command).

## Requirements

- Go matching `go.mod` to build from source (currently 1.27.1), and GNU Make for the development commands below.
- `gh` on your PATH, authenticated to the target host with `gh auth login` or an appropriate environment token.
- A `gh` version supporting `gh cache delete --all --succeed-on-no-caches`. Compatibility is checked before deletion.
- Access to discover the repositories and permission to delete their Actions caches. For classic tokens, the CLI documents the `repo` scope for deletion; team discovery may also require `read:org`. Fine-grained tokens need repository access and **Actions: write**, and appropriate organization permissions for team discovery. Organization SSO/token policies still apply. An ordinary workflow `GITHUB_TOKEN` is generally limited to its own repository, not your whole organization.

## Install from release assets

Prebuilt binaries are available in [release 1.0.0](https://github.com/benbenbang/gh-cache-sweep/releases/tag/1.0.0). No Go, Make, or mise installation is needed to use them; `gh` is still required for GitHub operations.

### Install with GitHub CLI

To register the command as `gh cache-sweep`, let GitHub CLI select and install the release asset:

```sh
gh extension install benbenbang/gh-cache-sweep --pin 1.0.0
gh cache-sweep --version
```

### Download with curl (macOS / Linux)

Choose the asset for your machine:

| Platform | Asset |
| --- | --- |
| macOS, Apple Silicon | `gh-cache-sweep-darwin-arm64` |
| macOS, Intel | `gh-cache-sweep-darwin-amd64` |
| Linux, x86-64 | `gh-cache-sweep-linux-amd64` |
| Linux, ARM64 | `gh-cache-sweep-linux-arm64` |
| Windows, x86-64 | `gh-cache-sweep-windows-amd64` |
| Windows, ARM64 | `gh-cache-sweep-windows-arm64` |

For example, install **1.0.0 on Apple Silicon macOS** below. Change `ASSET` to the appropriate name from the table for Intel macOS or Linux:

```sh
(
  set -eu
  VERSION=1.0.0
  ASSET=gh-cache-sweep-darwin-arm64
  mkdir -p "$HOME/.local/bin"
  TEMP_DIR=$(mktemp -d)
  trap 'rm -rf "$TEMP_DIR"' EXIT
  curl --fail --location --show-error --retry 3 \
    "https://github.com/benbenbang/gh-cache-sweep/releases/download/$VERSION/$ASSET" \
    --output "$TEMP_DIR/gh-cache-sweep"
  install -m 755 "$TEMP_DIR/gh-cache-sweep" "$HOME/.local/bin/gh-cache-sweep"
)
```

The download completes in a temporary directory before replacing an existing installation. No `sudo` is needed. Add `~/.local/bin` to your `PATH`, or run the binary by its full path:

```sh
"$HOME/.local/bin/gh-cache-sweep" --version
"$HOME/.local/bin/gh-cache-sweep" --org my-org --dry-run
```

This installs the **standalone `gh-cache-sweep` executable**, not a registered `gh` extension. Use the GitHub CLI installation above if you want `gh cache-sweep` with a space. To upgrade a curl installation, repeat the download with the desired release tag; `gh extension upgrade` does not manage it.

### Download with curl (Windows PowerShell)

The Windows release assets have no filename extension; save the download with an `.exe` suffix. For x86-64 Windows:

```powershell
curl.exe --fail --location --show-error --retry 3 --output gh-cache-sweep.exe https://github.com/benbenbang/gh-cache-sweep/releases/download/1.0.0/gh-cache-sweep-windows-amd64
if ($LASTEXITCODE -ne 0) { throw "Download failed" }
.\gh-cache-sweep.exe --version
.\gh-cache-sweep.exe --org my-org --dry-run
```

For ARM64 Windows, use the `gh-cache-sweep-windows-arm64` asset instead. Move the executable to a directory on your `PATH` to run it from anywhere.

## Try from source

From this checkout:

```sh
make run ARGS='--help'
make run ARGS='--org my-org --dry-run'
```

Both commands are non-destructive. Preview performs authenticated repository discovery but does not list individual caches or estimate disk savings.

## Install as a local gh extension

The checkout directory must be named **`gh-cache-sweep`**, with a binary of the same name at its root. This checkout may currently be named `gh-cache`; rename the directory before local installation to avoid colliding with the built-in `gh cache` command.

From the `gh-cache-sweep` directory:

```sh
make install
gh cache-sweep --help
```

On Windows, `make build-local` produces `gh-cache-sweep.exe`. Rebuild after changing source with `make build-local`; a local extension installation points to that binary. For installation without building from source, use the release assets described above.

## Usage

Choose exactly one scope. Flags can appear in any order; positional arguments are not accepted.

```sh
# Preview all visible repositories owned by an organization (the default is safe)
gh cache-sweep --org my-org

# Explicitly approve deletion
gh cache-sweep --org my-org --yes

# Your own repositories, including private repositories visible to your token
gh cache-sweep --user @me --dry-run

# Repositories owned by another user where you are a collaborator
gh cache-sweep --user alice --yes

# Repositories accessible to a team; use its slug, not its display name
gh cache-sweep --team my-org/platform --dry-run

# One repository
gh cache-sweep --repo my-org/service --yes

# Optional archived-repository exclusion and longer per-command timeout
gh cache-sweep --org my-org --skip-archived --timeout 20m --yes

# GitHub Enterprise; GH_HOST is also respected
gh cache-sweep --org my-org --hostname github.example.com --dry-run
```

`--dry-run` always overrides `--yes`. There is no interactive confirmation: deletion requires `--yes`, including in automation.

## Scope and safety

- **All pages, no fixed repository cap:** uses `gh api --paginate` with 100 repositories per page rather than `gh repo list --limit 1000`.
- **Fail-closed discovery:** API and JSON errors abort before any deletion, even if some pages were already retrieved. The complete repository list is validated, deduplicated, and sorted first.
- **Organization:** repositories owned by the organization and visible to the current token. GitHub can silently omit repositories your token cannot see; this tool cannot certify organization-wide completeness.
- **User:** filters authenticated ownership/collaborations by the requested owner. Unlike the public `/users/USER/repos` endpoint, this can include private repositories. Another user's public repositories where you are not a collaborator are not selected. `@me` resolves the authenticated account.
- **Team:** repositories the API reports as accessible to the team. Teams do not own repositories; this does not traverse team members' personal repositories. Read access via a team does not imply permission to delete caches.
- **Archived repositories and forks:** included unless `--skip-archived` excludes archived repositories. API permission/read-only failures are reported, not silently treated as success.
- **Sequential cleanup:** avoids introducing parallel API bursts. Each repository uses `gh cache delete --all --succeed-on-no-caches --repo HOST/OWNER/REPO`.
- **Empty caches succeed:** the success count includes repositories with no caches; it is not a count of deleted caches.
- **Partial failure is visible:** failures print `FAILED OWNER/REPO`, remaining repositories are attempted, and the process exits nonzero. The summary includes selected, succeeded, failed, and unattempted counts. A failed repository may already have had some caches deleted.
- **Bounded operations:** the default timeout is 10 minutes per `gh` command (including all discovery pages), not for the entire sweep. Ctrl-C cancels the active command and stops further deletions.
- **No automatic retry:** rate limits, authorization failures, and timeouts are reported. For rate limits, wait before rerunning; repeated deletion would also delete caches newly created since the prior run.

Deletion is irreversible and can slow subsequent workflow runs. This is not an atomic operation: active workflows may create caches during or after cleanup, so a successful sweep does not guarantee the organization remains cache-free. Pause cache-producing workflows if you need a quiet cleanup window. This deletes Actions caches, **not** workflow runs, artifacts, packages, or runner-local caches.

The explicit hostname is used for both discovery and deletion; `GH_REPO` or the repository in your working directory does not change the target.

## Build metadata

`gh cache-sweep --version` prints the version, UTC build time, and repository URL without requiring `gh` authentication or making API calls. `--version` exits without cleanup even if `--yes` is also supplied.

`metadata.go` defines the linker-injectable string variables `main.Version`, `main.BuildTime`, and `main.ProjectUrl`. Plain `go build` / `go run .` use `dev`, `unknown`, and this project's repository URL as defaults. Run the whole package (`go run .`), not just `main.go`, so metadata is included.

The makefile injects the version from the latest tag (falling back to the commit), the current UTC timestamp, and `ProjectUrl`:

```sh
make build-platform
# Or explicitly supply release metadata (also useful for reproducible builds):
make build-platform VERSION=v1.2.3 BuildTime=2026-09-10T12:34:56Z ProjectUrl=https://github.com/benbenbang/gh-cache-sweep
```

Platform binaries are written to `build/gh-cache-sweep-GOOS-GOARCH`. Use `make build-local` to build directly at the checkout root for local extension installation.

Build and run targets share the same linker flags, so metadata injection is maintained in one place. To inspect metadata with explicit overrides:

```sh
make version VERSION=v1.2.3 BuildTime=2026-09-10T12:34:56Z ProjectUrl=https://github.com/benbenbang/gh-cache-sweep
```

## Development

Use `mise r TASK` (`r` is short for `run`). Mise activates the pinned Go toolchain and forwards each task to its matching Make target:

```sh
mise r help                 # List available Make targets
mise r check                # Format Go source, then run go vet
mise r test                 # Tests with race detection and coverage
mise r test ARGS='-run TestVersionInfo -count=1'
mise r run ARGS='--help'     # Run the CLI; pass flags through ARGS
mise r version              # Print injected metadata; no GitHub calls
mise r build-local          # Build the local extension binary
mise r build-platform       # Build for GOOS/GOARCH
mise r build                # Build all six platform/architecture combinations
mise r install              # Build and install the local gh extension
mise r build-local VERSION=v1.2.3
```

`check` applies formatting changes before running static analysis; it does not run tests. The individual `mise r fmt` and `mise r vet` tasks remain available.

The makefile remains the single source of implementation; `mise.toml` contains only one-line task mappings. GNU Make is still required under the hood. Direct `make TASK` commands continue to work, and `mise r make TARGET` remains available for targets without a short mapping.

Override `TEST_FLAGS` when needed, for example `mise r test TEST_FLAGS=-cover` to omit the race detector. `ARGS` is shell command-line text; only pass trusted values. Runtime safety rules still apply: `mise r run ARGS='--org my-org'` previews, and deletion requires explicit `--yes`.

Tests use a scripted `gh` runner: they require neither credentials nor network access and never delete real caches.

## CI and releases

PR Go checks run when Go sources, module files, `makefile`, `mise.toml`, or workflow YAML files change. The required success job accepts a skipped Go job only when change detection explicitly says checks are unnecessary; detection errors still fail the workflow. CI uses the same mise/Make commands as local development and rejects formatting changes.

To publish, dispatch **semantic-release** from `main` with `prompt=true` and leave `release_tag` empty. Tests, formatting checks, and all six platform builds must pass before semantic-release runs. Assets are then rebuilt from the exact published tag with its version injected and uploaded. The reusable release workflow requires the `TECHNICAL_APP_APP_ID` and `TECHNICAL_APP_PEM` secrets for its GitHub App; it still uses inherited secrets because the upstream workflow does not declare named secret inputs.

Publication and asset upload are not atomic. If the upload/build job fails after publication, rerun the failed job, or dispatch from `main` with `prompt=true` and `release_tag` set to the existing tag. Repair validates the existing release, checks out its tag, tests and rebuilds it, and replaces its six binary assets using `--clobber` without creating a new release. The tag must include the mise/Make build tasks used by this workflow. Release and repair runs share a concurrency group to prevent overlapping writes.

References: [`gh api`](https://cli.github.com/manual/gh_api), [`gh cache delete`](https://cli.github.com/manual/gh_cache_delete), [`gh extension install`](https://cli.github.com/manual/gh_extension_install).
