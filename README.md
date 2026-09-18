# github-docs-mcp

A read-only MCP server that lets your AI client search [GitHub documentation](https://docs.github.com) and read the pages it needs. It keeps cached copies available when GitHub Docs cannot be reached.

[Install](#install) · [Connect](#mcp-server) · [Tools](#tools) · [Configuration](#configuration) · [Verify releases](#verify-a-public-release)

> [!NOTE]
> Independent project, not affiliated with GitHub. Serves public docs without credentials or writes. For repositories, issues and pull requests, see [GitHub's MCP server](https://github.com/github/github-mcp-server).

## Install

Choose how to install the server:

| Method | Requirements and supported platforms |
| --- | --- |
| [Homebrew](#homebrew) | macOS Apple Silicon; Linux x86-64/ARM64 |
| [Container](#container) | Docker; see release manifests for available architectures |
| [Prebuilt binary](#prebuilt-binary) | macOS Apple Silicon; Linux and Windows x86-64/ARM64 |
| [From source](#from-source) | Compatible Go toolchain |

Then [connect your MCP client](#mcp-server). For development, see [CONTRIBUTING.md](CONTRIBUTING.md#local-setup).

### Container

Pull the stable image and check its version:

```sh
docker pull ghcr.io/matcra587/github-docs-mcp:latest
docker run --rm ghcr.io/matcra587/github-docs-mcp:latest -version
```

Use a version tag such as `v0.1.0` to select a release. To select an exact image, copy its digest reference from the release page.

### Homebrew

```sh
brew install matcra587/tap/github-docs-mcp
github-docs-mcp -version
```

To upgrade, run `brew upgrade matcra587/tap/github-docs-mcp`. To build the latest code from `main`, use `brew install --HEAD matcra587/tap/github-docs-mcp`.

#### Switching an existing installation to Homebrew

<details>
<summary>Homebrew installed, but the wrong version still runs?</summary>

Your shell may find an older installation first. Compare it with Homebrew's copy:

```sh
type -a github-docs-mcp
command -v github-docs-mcp
brew --prefix github-docs-mcp
"$(brew --prefix github-docs-mcp)/bin/github-docs-mcp" -version
```

| Cause | Fix |
| --- | --- |
| mise installation | Identify it with `mise which github-docs-mcp` and `mise ls --installed`. Remove its config entry, then uninstall using mise's exact tool identifier. |
| Stale mise shim | Run `mise reshim`. |
| Manual or `go install` binary | Back it up, then remove it from `PATH`. To preserve an existing absolute MCP path, replace it with a symlink to Homebrew's binary. |

The executable name may not be a mise registry name. `aqua:github/github-mcp-server` is a different server.

Recheck `command -v github-docs-mcp` and `github-docs-mcp -version`, update any absolute MCP path and restart the client. Hiding Homebrew's warning does not fix `PATH`.

</details>

### Prebuilt binary

Download an archive for your OS and architecture from
[Releases](https://github.com/matcra587/github-docs-mcp/releases):

| Platform | Archive suffix |
| --- | --- |
| Linux x86-64 | `linux_amd64.tar.gz` |
| Linux ARM64 | `linux_arm64.tar.gz` |
| macOS Apple Silicon | `darwin_arm64.tar.gz` |
| Windows x86-64 | `windows_amd64.zip` |
| Windows ARM64 | `windows_arm64.zip` |

[Verify the archive](#verify-a-public-release) before extracting it. Put
`github-docs-mcp` (or `github-docs-mcp.exe` on Windows) in a directory on your
`PATH`, then run `github-docs-mcp -version`. The verification section includes
an installation example for Linux and macOS.

### From source

Requires a Go toolchain compatible with [go.mod](go.mod):

```sh
go install github.com/matcra587/github-docs-mcp/cmd/github-docs-mcp@latest
```

The executable is installed into `GOBIN`, or `$(go env GOPATH)/bin` when
`GOBIN` is unset. Add that directory to your `PATH`, then check the installation:

```sh
github-docs-mcp -version
```

Replace `@latest` with a release tag such as `@v0.1.0` to pin the source version.
Restart your MCP client after upgrading so it launches the new executable.

## MCP server

Your client can start its own server over **stdio**, or connect to a shared server over **HTTP**. Choose one setup below.

### Container (stdio)

The client starts the container on demand. stdio is the default transport.

```sh
claude mcp add github-docs -- docker run -i --rm ghcr.io/matcra587/github-docs-mcp:latest
```

```json
{
  "mcpServers": {
    "github-docs": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "ghcr.io/matcra587/github-docs-mcp:latest"]
    }
  }
}
```

### Installed binary (stdio)

For Homebrew, prebuilt or source installations on your `PATH`:

```sh
claude mcp add github-docs -- github-docs-mcp
```

```json
{
  "mcpServers": {
    "github-docs": {
      "command": "github-docs-mcp"
    }
  }
}
```

> [!TIP]
> Client cannot find the binary? Set `command` to an absolute path. For Homebrew, append `/bin/github-docs-mcp` to `brew --prefix` output: typically `/opt/homebrew` on Apple Silicon or `/home/linuxbrew/.linuxbrew` on Linux. Paste the resolved path; JSON does not expand shell commands. Restart the client after upgrades.

Native sessions reuse the disk cache automatically; no extra MCP arguments needed. See [cache behaviour](#cache-behaviour).

### Shared server (HTTP)

> [!WARNING]
> HTTP has **no authentication**. Anyone who can reach the server can use it. Keep it on a trusted network and restrict access with bind addresses, port mappings and firewalls.

Compose sets `MCP_TRANSPORT=http` and serves `/mcp`. Run from a [checkout](CONTRIBUTING.md#local-setup):

```sh
docker compose up -d
claude mcp add --transport http github-docs http://127.0.0.1:8080/mcp
```

```json
{
  "mcpServers": {
    "github-docs": {
      "type": "http",
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

### Codex

Choose one transport. The CLI writes `~/.codex/config.toml`:

```sh
# Container-backed stdio
codex mcp add github-docs -- docker run -i --rm ghcr.io/matcra587/github-docs-mcp:latest

# Locally installed binary
codex mcp add github-docs -- github-docs-mcp

# Running HTTP server
codex mcp add github-docs --url http://127.0.0.1:8080/mcp
```

## Tools

| Tool | Arguments | Returns |
| --- | --- | --- |
| `list_docs` | `section?`, `limit?` (default 50, max 200) | Page slugs, titles and descriptions |
| `search_docs` | `query`, `limit?` (default 10, max 50) | Ranked matches with section paths and text snippets |
| `get_doc` | `slug`, `heading?`, `query?`, `offset?` | A Markdown page or selected sections |

Arguments ending in `?` are optional. A slug identifies a page; a full GitHub Docs URL also works.

For a specific question, use `get_doc` with `heading` or `query` to read only the relevant sections. Text comes directly from the docs, without summarisation. Long pages arrive in 50KB chunks; use the returned `offset` to continue reading.

Search results show page sizes when a cached copy is available. Sizes may be out of date; unknown sizes stay unknown rather than triggering extra downloads. If GitHub Docs cannot be reached, cached pages are returned with a note that they may be stale.

### Where results come from

<details>
<summary>GitHub Docs endpoints and offline search</summary>

The server uses these public endpoints:

| Origin endpoint | Used for | Note |
| --- | --- | --- |
| `/llms.txt` | catalogue titles and descriptions | curated shortlist, ~120 entries |
| `/api/pagelist/en/free-pro-team@latest` | the authoritative set of valid paths | ~3200 paths, no prose |
| `/api/search/v1` | `search_docs` | server-side index over every page body |
| `/<path>.md` | `get_doc` | the bare path serves rendered HTML; only `.md` serves markdown |

The page list combines the first two endpoints. Curated pages keep their titles and descriptions; other valid paths remain available to `get_doc`. You can use a slug, a path such as `/en/actions`, or a full docs URL, including `.md` and `#anchor` suffixes.

`search_docs` uses GitHub's search endpoint to find pages, including those never fetched by this server. If that endpoint is unavailable, it searches the page list and cached text instead. Offline search therefore covers fewer pages.

</details>

## Configuration

Flags override environment variables.

| Env | Flag | Default | Purpose |
| --- | --- | --- | --- |
| `MCP_TRANSPORT` | `-transport` | `stdio` | `stdio` or `http`; Compose selects `http` |
| `MCP_HTTP_ADDR` | `-http-addr` | `127.0.0.1:8080` (binary), `0.0.0.0:8080` (image) | HTTP listen address |
| `MCP_ALLOWED_ORIGINS` | `-allowed-origins` | localhost only | extra browser `Origin` allow-list (comma-separated) |
| `DOCS_BASE_URL` | `-base-url` | `https://docs.github.com` | docs origin (`https` required; `http` for loopback fixtures) |
| `DOCS_INDEX_TTL` | `-index-ttl` | `1h` | how long the page list stays fresh |
| `DOCS_PAGE_TTL` | `-page-ttl` | `24h` | how long pages stay fresh |
| `DOCS_FETCH_RPS` | `-fetch-rps` | `2` (burst 2×) | requests per second to GitHub Docs |
| `DOCS_CACHE_MAX_BYTES` | `-cache-max-bytes` | `64MiB` | memory limit; least recently used entries removed first |
| `DOCS_CACHE_DIR` | `-cache-dir` | user cache directory + `/github-docs-mcp` | disk cache survives restarts; empty disables it |
| `LOG_LEVEL` | `-log-level` | `info` | log detail; JSON logs on stderr |

### Cache behaviour

| Setting | Behaviour |
| --- | --- |
| Default directory | `os.UserCacheDir()/github-docs-mcp`, e.g. `$XDG_CACHE_HOME/github-docs-mcp` on Linux |
| Custom directory | `DOCS_CACHE_DIR` or `-cache-dir`; explicitly empty disables persistence |
| Disk budget | 256 MiB of persisted values; oldest page writes pruned before catalogue entries after each store |
| Concurrent processes | File lock covers reads, writes and pruning; waits at most one second |
| Disk failure | Memory cache remains usable; unresolved user cache directory also means memory-only caching |
| Alternate origins | Separate cache subdirectories per `-base-url` |
| Containers | `--rm` discards the container filesystem; use the Compose cache volume for persistence |

Use a local filesystem with working file locks and upgrade every process sharing the cache; older versions do not participate in locking. The disk budget excludes temporary files and filesystem overhead. The cache is not durable storage.

<details>
<summary>Debug logs and tool-result cache metadata</summary>

With `-log-level debug`, stderr includes cache decisions (`hit`, `miss`,
`expired`, `stale-serve`, `write`, `write-skipped`, `write-failed`, `eviction`)
with the entry key, source and age. Tool results also include `_meta.cache`,
an array containing the final decision for each catalogue or page entry used.
Each entry has `key`, `status`, `source`, `age_ms` and `remaining_ttl_ms`.
Missing entries have null age and TTL; expired entries have zero remaining TTL.
A `miss` describes the entry before fetching. The source `disk` means the entry
was loaded from disk at startup; subsequent writes use `memory`. Search
results themselves are not cached: `search` reports `bypass` for origin results
or `fallback` for local results, with null age and TTL. Offline results also
include decisions for cached page bodies used to produce returned snippets.

</details>

No credentials are needed or accepted. The server limits requests and honours `Retry-After` when GitHub asks it to wait. These public docs endpoints do not provide the `x-ratelimit-*` headers used by `api.github.com`.

## Protocol

<details>
<summary>MCP version negotiation, and what 2026-07-28 changes here</summary>

Built on the official [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk),
which negotiates MCP **2026-07-28** and falls back through `2025-11-25`,
`2025-06-18`, `2025-03-26` and `2024-11-05`.

Clients using the older `initialize` request negotiate at most `2025-11-25`. Clients using the newer handshake can use `2026-07-28`.

The SDK checks required arguments before running a tool. Missing `query` or `slug` returns an error naming the field.

What `2026-07-28` changes for this server specifically:

| Spec change | Here |
| --- | --- |
| Stateless core, no handshake ([SEP-2575]) | over HTTP, `tools/list` and `tools/call` work with no `initialize`, because it runs `Stateless: true`, so there is no session table. stdio still requires the handshake: the SDK rejects a call made during session initialization |
| `ttlMs` / `cacheScope` **required** on list results ([SEP-2549]) | `tools/list` returns `ttlMs: 3600000`, `cacheScope: "public"` |
| Deterministic `tools/list` order | sorted by name, which is what makes the cache hint safe |
| Logging deprecated ([SEP-2577]) | capability not advertised; diagnostics go to stderr, as the spec now directs |
| Full JSON Schema 2020-12 ([SEP-2106]) | input schemas inferred from Go structs |

`server/discover` deliberately carries no TTL: it is where protocol-version
negotiation happens, and a stale copy could make a client talk an old version
to a freshly deployed server. The SEP does not ask for one.

[SEP-2575]: https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2575
[SEP-2549]: https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2549
[SEP-2577]: https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2577
[SEP-2106]: https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2106

</details>

## Deployment

[docker-compose.yml](docker-compose.yml) provides a single-host deployment with a read-only root filesystem, `cap_drop: ALL`, `no-new-privileges`, loopback port binding and a cache volume.

`/healthz` reports whether the server is running. It stays healthy when GitHub Docs is unavailable so clients can still read cached pages.

## License

The server is [MIT licensed](LICENSE). Copied GitHub documentation content in
the test fixtures retains its upstream CC BY 4.0 license; see
[third-party notices](THIRD_PARTY_NOTICES.md) for sources, modifications and scope.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for local development, testing, dependency maintenance and release procedures.

## Verify a public release

Requires GitHub CLI, Cosign 3 and a SHA-256 utility. Run in an empty directory.

### Binary archives

Set `tag`, then verify the checksum signature, archive checksum and build provenance:

```sh
repo=matcra587/github-docs-mcp
tag=v0.1.0 # choose an existing release from the releases page
archive="github-docs-mcp_${tag#v}_linux_amd64.tar.gz"
identity="https://github.com/${repo}/.github/workflows/release.yml@refs/tags/${tag}"

gh release download "$tag" --repo "$repo" \
  --pattern "$archive" --pattern checksums.txt --pattern checksums.txt.sigstore.json
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
awk -v name="$archive" '$2 == name' checksums.txt | sha256sum --check --strict
gh attestation verify "$archive" --repo "$repo" \
  --cert-identity "$identity" --source-ref "refs/tags/${tag}" \
  --deny-self-hosted-runners
```

On macOS, substitute `shasum -a 256 --check` for `sha256sum --check --strict`.

> [!IMPORTANT]
> Stop if verification fails. Verify before extracting: the archive attestation does not apply directly to the executable.

After verification, Linux and macOS users can install the selected `.tar.gz`
archive into a user-owned directory. On macOS, select the `darwin_arm64`
archive in the download example first.

```sh
tar -xzf "$archive" github-docs-mcp
install_dir="$HOME/.local/bin"
mkdir -p "$install_dir"
install -m 755 github-docs-mcp "$install_dir/github-docs-mcp"
"$install_dir/github-docs-mcp" -version
```

Add that directory to your `PATH`, or use the executable's absolute path in
your MCP client configuration. On Windows, extract the verified `.zip` and
place `github-docs-mcp.exe` in a directory on your user `Path`.

### Container images

Keep `repo`, `tag` and `identity` from the preceding example set in the same shell.
For a container, copy the full `image:tag@sha256:...` reference from the release's
Docker Manifests section. Verify the digest with both tools before running it:

```sh
image='ghcr.io/matcra587/github-docs-mcp:v0.1.0@sha256:REPLACE_WITH_RELEASE_DIGEST'
cosign verify "$image" --certificate-identity "$identity" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify "oci://${image}" --repo "$repo" \
  --cert-identity "$identity" --source-ref "refs/tags/${tag}" \
  --deny-self-hosted-runners
```

Signatures identify the publishing workflow; provenance links artifacts to
their source and build. Neither guarantees vulnerability-free software.

## Design

<details>
<summary>The four rules the implementation follows</summary>

**Stale beats error.** Documentation that is a day old is useful; an error is
not. Entries past their TTL are kept, not evicted, and served with a note when
the origin cannot be reached. The default disk cache carries that across
restarts, so a process starting during an outage still has something to serve.

**One fetch per miss.** Concurrent callers for the same page share a single
request through singleflight, on a detached context, so one caller cancelling
must not fail the others. After an origin failure a short cool-down serves
cached content immediately rather than paying full retry latency per request.

**Bounded everything.** Body size caps per endpoint, an LRU byte cap on the
memory cache, an outbound token bucket, jittered retry honouring `Retry-After`,
and a 50KB window on returned pages with a continuation offset.

**Trust boundary.** The origin is the only external dependency and is treated as
untrusted input: redirects may not leave the base host or downgrade the scheme,
cookies are never stored, an HTML body on a `200` is refused outright, and a
catalogue that parses to zero entries is an error rather than an empty
catalogue. No credentials are held, so there is nothing to leak; the HTTP
transport is unauthenticated by design and gated on reachability.

</details>
