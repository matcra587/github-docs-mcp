# Contributing

For published binaries, containers and MCP client configuration, see the
[README installation guide](README.md#install). This guide covers working on
the source repository.

## Local setup

Install [mise](https://mise.jdx.dev/getting-started.html) and Git first. Race tests require a C compiler and linker; CI and release
checks also require Docker with Compose and Buildx. The optional MCP Inspector
requires Node.js 22.19 or newer and `npx`. Clone the repository and prepare the checkout:

```sh
git clone https://github.com/matcra587/github-docs-mcp.git
cd github-docs-mcp
mise trust
mise install --locked
mise run hooks:install
mise run build
./bin/github-docs-mcp -version
```

`mise run build` writes the development binary to `bin/`. To install the
checkout into Go's binary directory instead:

```sh
mise exec -- go install ./cmd/github-docs-mcp
```

Ensure `GOBIN` (or `$(go env GOPATH)/bin` when `GOBIN` is unset) is on your `PATH` before configuring
your MCP client. Restart the client after rebuilding an executable it uses.
To return to a release build, follow the [installation guide](README.md#install).

## Checks

[Tasks](tasks.toml) define the commands used locally and in CI. Run them from
the repository root:

```sh
mise run test       # unit + in-process integration; zero network
mise run check      # hk checks + offline race tests
mise run ci         # exactly what GitHub CI runs (same task, via mise-action)
mise run fix        # hk fixes; leaves changes unstaged
```

hk owns formatting, linting, module checks and Git hooks. Pre-commit runs
file checks; pre-push and `mise run check` also run module analysis and
vulnerability checks. Use `mise exec -- hk check --all --step <name>` for an individual
check, or `mise run hooks:install` to install hooks explicitly.
GoReleaser is scoped to release tasks; mise uses the locked tool versions.

Offline testing with the committed fixtures, using two terminals:

```sh
# Terminal 1: keep the fixture server running
mise run fixture-serve

# Terminal 2: start the stdio server with the fixture origin
DOCS_CACHE_DIR= DOCS_BASE_URL=http://127.0.0.1:9999 mise run run
```

The fixture example disables disk caching to keep development content isolated.
The stdio process expects MCP messages, not interactive shell commands.
For an interactive client, use MCP Inspector in terminal 2 instead:

```sh
mise exec -- npx --yes @modelcontextprotocol/inspector --web -e DOCS_CACHE_DIR= -e DOCS_BASE_URL=http://127.0.0.1:9999 go run ./cmd/github-docs-mcp
```

Omit `-e DOCS_BASE_URL=http://127.0.0.1:9999` to inspect the live GitHub documentation instead.

Live-origin drift canaries (also run nightly in CI):

```sh
mise run test:integration
```

## Dependency maintenance

Dependabot proposes Go module, GitHub Actions and container base updates.
Clover provides manual updates for annotated pins, including mise tools and
workflow references, using the existing policy that allows major upgrades.
The hk binary and Pkl imports follow one version; the container base follows
the `nonroot` tag while retaining a digest pin.

```sh
mise run clover:check          # annotation lint, formatting and coverage
mise exec -- clover run --dry-run
mise exec -- clover run        # apply reviewed version updates
mise lock --bump               # refresh tool artifacts, including latest selectors
mise install --locked
mise run ci
```

Compatibility floors, schema versions, protocol dates and frozen documentation
fixtures are reviewed manually. Tools declared as `latest` retain that selector;
`mise.lock` records the exact artifacts used by CI. Go module dependencies and
`go.sum` remain managed by Go and Dependabot. Clover does not run a second
scheduled updater.

## Releasing

<details>
<summary>How a release is cut</summary>

GoReleaser owns the whole release: binaries, archives, `checksums.txt` and the
multi-arch image. `docker build .` cannot build the image, because the Dockerfile
copies binaries GoReleaser cross-compiles into a temporary context.

```sh
mise run release:snapshot   # full local build incl. image, nothing published
mise run release:check      # validate .goreleaser.yaml
mise run release -- --dry-run v1.2.3 # preflight without creating or pushing a tag
mise run release -- v1.2.3           # preflight, then tag and push (releases from main)
```

When a commit argument selects an older commit, preflight runs in a temporary
worktree at that commit before creating its tag.

Pushing the tag is the trigger. The workflow waits for the `ci` and `security`
gates covering that exact commit before it publishes anything.

Release notes group commit subjects into breaking changes, features, fixes,
performance, dependencies and maintenance. They retain PR references and omit
commit hashes. Routine documentation, formatting and test commits are excluded,
including scoped subjects; a `!` marker keeps a breaking change visible.

Write squash commit subjects around the observable outcome, for example
`fix(release): keep prereleases out of the latest release`. The generator
groups those subjects; it does not rewrite them into prose.

For a release with reviewed notes, commit a Markdown file
at `.github/release-notes/<tag>.md` before tagging, for example
`.github/release-notes/v1.2.3.md`. The publishing task uses that file instead of
the generated commit list. Write categorized, user-facing changes with PR
references, and include upgrade instructions when needed. Each file belongs
to one exact tag, so old notes cannot carry into another release. Without a
matching file, the grouped changelog is published. An empty file stops
publication. Both formats include the versioned container reference. See the
[release-note guide](.github/release-notes/README.md) for the format.

GoReleaser signs `checksums.txt` and the multi-platform container digest on fresh public releases. Releases stay in draft until the finalization workflow verifies both signatures, generates GitHub provenance for all five archives and the container, and verifies that provenance. The checksum
signature is attached as `checksums.txt.sigstore.json`. `digests.txt` records
the published image references. Private repositories pass `--skip=sign` to GoReleaser and skip Cosign setup and attestation. Local snapshots also pass `--skip=sign`; neither path contacts public signing services.

`tasks.toml` owns the release commands: `release:state`, `release:subjects`,
`release:recover-signatures`, `release:verify-signatures`, `release:verify`, and
`release:finalize`. The workflow supplies CI credentials and attestation bundle
paths and guards Cosign setup and the finalization composite by repository visibility and release state. Fresh releases use GoReleaser signing; the composite verifies signatures, attests provenance, verifies provenance, and publishes in order.
Longer task implementations live under `.mise/tasks/`. `mise run release:test` validates release subjects and signing orchestration offline, substituting external signing and publishing commands.

A failure before GoReleaser creates the draft can leave no release; a retry rebuilds in that case. Once a draft exists, rerunning downloads and checks its existing assets, recovers checksum and image signatures, and repeats verification and attestation without rebuilding. A failure during finalization leaves the release as a draft. Missing or corrupt assets stop the retry.
An incomplete GoReleaser upload needs investigation and repair before retrying.
Published releases are left unchanged. Container images are already pushed
before signing; consumers must verify them before use. A draft GitHub release
does not hide the corresponding registry image.

The workflow follows GitHub's [artifact attestation guidance](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations)
and [draft-first guidance for immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases).
Repository administrators must enable release immutability separately. The
workflow does not change repository or package visibility. BuildKit SBOM and
provenance metadata remain enabled, but do not replace publisher signatures.

Before the first release, enable Actions before pushing the final commit to `main`. The release gate requires successful push-triggered `ci` and `security` runs for the exact tagged commit; enabling Actions after a push does not create those runs. Confirm that the release repository has write access to the GHCR package. Package visibility is separate from repository visibility, and making an existing package public exposes its existing versions as well. Review package contents before changing visibility.

After a public stable release is finalized, the shared `homebrew-publish-formula`
action updates `Formula/github-docs-mcp.rb` in `matcra587/homebrew-tap` using the
published archive checksums. Private releases and prereleases skip this step.
For releases whose tagged workflow includes Homebrew publishing, a workflow rerun
can retry the tap update without rebuilding or changing the published release.
The existing `v0.2.0` formula is seeded separately because that tag predates this
workflow change.

Configure the packaging App's `APP_CLIENT_ID` variable and `APP_PRIVATE_KEY`
secret in the `deploy` environment. The App must be installed on `homebrew-tap`
with contents write permission. The workflow requests a short-lived token scoped
to that tap, matching the Jira release setup. `release:homebrew-checksums` rejects
drafts and prereleases before downloading checksums.

The formula supports release archives and `brew install --HEAD matcra587/tap/github-docs-mcp`. HEAD builds use Homebrew's Go build arguments;
version reporting falls back to Go build metadata. Shell completions are omitted
because this server does not expose a completion command.

</details>
