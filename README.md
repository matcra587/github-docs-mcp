# github-docs-mcp

An MCP server exposing the [GitHub documentation](https://docs.github.com) to
any MCP client. Three read-only tools (`list_docs`, `search_docs`, `get_doc`)
backed by a live fetch of the docs site with a stale-servable TTL cache,
built-in rate limiting and retry, over stdio or streamable HTTP.

> [!NOTE]
> Independent project; not affiliated with or endorsed by GitHub.
> Not to be confused with [github/github-mcp-server](https://github.com/github/github-mcp-server),
> GitHub's own MCP server for repositories, issues and pull requests. This one
> serves the **documentation** and nothing else: no credentials, no writes.

> [!WARNING]
> The HTTP transport is **unauthenticated by design**: it serves public
> documentation, and reachability (bind address, port mapping, firewall) is the
> access control. Do not expose it beyond networks you trust to reach it.

## Install

Choose Homebrew, a published binary, a container or a source installation. After installing,
[configure your MCP client](#mcp-server). For a development checkout, see
[CONTRIBUTING.md](CONTRIBUTING.md#local-setup).

### Container

Requires Docker. Pull the current stable image and check its version:

```sh
docker pull ghcr.io/matcra587/github-docs-mcp:latest
docker run --rm ghcr.io/matcra587/github-docs-mcp:latest -version
```

Use a version tag such as `v0.1.0` instead of `latest` to select a specific
release. The release page also provides immutable image digest references.

### Homebrew

Supports macOS Apple Silicon and Linux x86-64/ARM64:

```sh
brew install matcra587/tap/github-docs-mcp
github-docs-mcp -version
```

Upgrade with `brew upgrade matcra587/tap/github-docs-mcp`. To build the latest
`main` from source instead, use `brew install --HEAD matcra587/tap/github-docs-mcp`.
Then use the [native MCP configuration](#mcp-server).

#### Switching an existing installation to Homebrew

An older executable or mise shim earlier on `PATH` can still take precedence.
Check which executable your shell finds and compare it with Homebrew's copy:

```sh
type -a github-docs-mcp
command -v github-docs-mcp
brew --prefix github-docs-mcp
"$(brew --prefix github-docs-mcp)/bin/github-docs-mcp" -version
```

If mise shadows the Homebrew binary, run `mise which github-docs-mcp` and
`mise ls --installed` to identify its source. For a mise-managed installation,
remove its entry from the relevant mise configuration and uninstall using the
exact tool identifier shown by mise; the executable name may not be a registered
tool name. If only a stale shim remains, run `mise reshim`.
`aqua:github/github-mcp-server` is a different server, not this project.

For a manually installed or `go install` copy, back up the older executable and
remove it from `PATH`, or replace it with a symlink to Homebrew's binary if an
existing MCP configuration uses that absolute path. Check `command -v` and
`github-docs-mcp -version` again. Hiding Homebrew's shadow warning does not change
which executable runs.

Update any absolute executable path in your MCP client configuration and restart
the client. Clients that do not inherit your shell's `PATH` should use an absolute
path as described below.

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

**stdio** (default). The client starts the container on demand, no
configuration needed because the image already defaults to this transport:

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

**stdio, no container.** For a Homebrew, prebuilt or source-installed binary on your `PATH`:

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

If the client cannot find the executable, set `command` to its absolute path.
For Homebrew, run `brew --prefix` and append `/bin/github-docs-mcp` to that output
(typically `/opt/homebrew/bin/github-docs-mcp` on Apple Silicon or
`/home/linuxbrew/.linuxbrew/bin/github-docs-mcp` on Linux). Paste the resolved
path into the configuration; JSON does not expand shell commands. Restart the
MCP client after installing or upgrading the executable.

Native clients automatically reuse `os.UserCacheDir()/github-docs-mcp` across
sessions (for example, `$XDG_CACHE_HOME/github-docs-mcp` on Linux). No additional
MCP arguments are needed. Set `DOCS_CACHE_DIR` or pass `-cache-dir` to choose a
location; explicitly setting either to an empty string disables persistence.
If the user cache directory cannot be resolved, caching stays memory-only.
Disk failures do not prevent serving documentation. Alternate `-base-url` origins
use separate cache subdirectories.

Persisted values use a 256 MiB budget, pruning oldest page writes before catalogue entries after each store.
Disk reads, writes and pruning share an operating-system lock across processes.
Lock acquisition waits at most one second; failures leave the in-memory cache usable.
Use a local filesystem with working file locks. Older versions do not participate
in this locking protocol; upgrade all processes sharing the directory. The budget
excludes temporary files and filesystem overhead. This cache is not a durable store.
Container filesystems disappear with
`--rm`; use the Compose cache volume for persistence across container restarts.

**streamable HTTP.** A shared, long-running server at `/mcp`. The image needs
`MCP_TRANSPORT=http` to serve it, which `docker-compose.yml` already sets.
Run from a checkout (see [local setup](CONTRIBUTING.md#local-setup)):

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

For Codex, use its CLI to write `~/.codex/config.toml`. Choose one transport:

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
| `list_docs` | `section?`, `limit?` (default 50, max 200) | catalogue: slug, title, description |
| `search_docs` | `query`, `limit?` (default 10, max 50) | ranked hits with breadcrumbs and a matching-content snippet |
| `get_doc` | `slug`, `heading?`, `query?`, `offset?` | page markdown; `heading` extracts one named section, `query` returns only the sections matching keywords (verbatim, with breadcrumbs; cheapest way to pull one fact from a long page), `offset` continues a truncated page |

Search results include cached page byte counts when available. Uncached sizes
are marked unknown; search never downloads pages just to measure them. Cached
sizes may differ from the current origin. Use `get_doc` with `query` or `heading`
to retrieve focused content from a large page.

Pages are windowed at 50KB: a notice at the top shows the byte range and a
marker at the bottom gives the `offset` to continue from (or suggests
`heading` for a single section).
When the docs origin is unreachable and a cached copy exists, the copy is
served with a staleness note: stale beats error for documentation.

For a focused question, `get_doc` with a `query` returns only the matching
sections verbatim, a fraction of a long page's tokens, with no external
calls, no credentials, and no summarization: the bytes are the docs' own.

### How the origin is read

<details>
<summary>Which docs.github.com endpoints back which tool, and why</summary>

`docs.github.com` publishes several things this server uses, and the split
matters for what each tool can see:

| Origin endpoint | Used for | Note |
| --- | --- | --- |
| `/llms.txt` | catalogue titles and descriptions | curated shortlist, ~120 entries |
| `/api/pagelist/en/free-pro-team@latest` | the authoritative set of valid paths | ~3200 paths, no prose |
| `/api/search/v1` | `search_docs` | server-side index over every page body |
| `/<path>.md` | `get_doc` | the bare path serves rendered HTML; only `.md` serves markdown |

The catalogue is the **merge** of the first two: curated entries keep their
human-written titles and descriptions, and every other real path is still a
valid `get_doc` slug, so the cross-links inside GitHub's own pages resolve.
Slugs are forgiving: a bare slug, an absolute path (`/en/actions`), a full
docs URL, a trailing `.md` or a `#anchor` all work.

`search_docs` delegates to the origin's search endpoint rather than scoring
locally, because this origin publishes no bundled full-text file. That is the
only way search covers pages nobody has fetched. If the origin is unreachable,
search degrades to local scoring over catalogue metadata plus already-cached
page bodies: narrower, but an answer rather than an error.

</details>

## Configuration

Every knob is an environment variable with a flag override (flag wins).

| Env | Flag | Default | Purpose |
| --- | --- | --- | --- |
| `MCP_TRANSPORT` | `-transport` | `stdio` | `stdio` or `http`; the image defaults to `stdio` too, so `docker run -i` works unconfigured; `docker compose up` asks for `http` explicitly |
| `MCP_HTTP_ADDR` | `-http-addr` | `127.0.0.1:8080` (binary), `0.0.0.0:8080` (image) | HTTP listen address |
| `MCP_ALLOWED_ORIGINS` | `-allowed-origins` | localhost only | extra browser `Origin` allow-list (comma-separated) |
| `DOCS_BASE_URL` | `-base-url` | `https://docs.github.com` | docs origin (`https` required; `http` for loopback fixtures) |
| `DOCS_INDEX_TTL` | `-index-ttl` | `1h` | index freshness window |
| `DOCS_PAGE_TTL` | `-page-ttl` | `24h` | page freshness window |
| `DOCS_FETCH_RPS` | `-fetch-rps` | `2` (burst 2×) | outbound token bucket |
| `DOCS_CACHE_MAX_BYTES` | `-cache-max-bytes` | `64MiB` | memory cache LRU byte cap |
| `DOCS_CACHE_DIR` | `-cache-dir` | user cache directory + `/github-docs-mcp` | disk cache survives restarts; empty disables it |
| `LOG_LEVEL` | `-log-level` | `info` | slog level; JSON logs on stderr |

With `-log-level debug`, stderr includes cache decisions (`hit`, `miss`,
`expired`, `stale-serve`, `write`, `write-skipped`, `write-failed`, `eviction`)
with the entry key, source and age. Tool results also include `_meta.cache`,
an array containing the final decision for each catalogue or page entry used.
Each entry has `key`, `status`, `source`, `age_ms` and `remaining_ttl_ms`.
Missing entries have null age and TTL; expired entries have zero remaining TTL.
A `miss` describes the entry before fetching. The source `disk` means the entry
was hydrated from disk at startup; subsequent writes use `memory`. Search
results themselves are not cached: `search` reports `bypass` for origin results
or `fallback` for local results, with null age and TTL. Offline results also
include decisions for cached page bodies used to produce returned snippets.

No credentials are needed or accepted. These docs endpoints are public and,
unlike `api.github.com`, publish no `x-ratelimit-*` headers; the outbound token
bucket plus 429/`Retry-After` handling is what keeps this a good citizen.

## Protocol

<details>
<summary>MCP version negotiation, and what 2026-07-28 changes here</summary>

Built on the official [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk),
which negotiates MCP **2026-07-28** and falls back through `2025-11-25`,
`2025-06-18`, `2025-03-26` and `2024-11-05`.

Note that `2026-07-28` deprecates the `initialize` request; a client still
using it is capped at `2025-11-25` by the SDK, which is correct behaviour
rather than a downgrade. Clients on the newer handshake get `2026-07-28`.

Each tool's input schema is inferred from a Go struct, so required arguments
are validated by the SDK before a handler runs, so a call missing `query` or
`slug` comes back as a tool error naming the field.

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

* `docker-compose.yml`: hardened single-host run: read-only rootfs,
  `cap_drop: ALL`, `no-new-privileges`, loopback port binding, cache volume.

The HTTP transport serves `/healthz` for process liveness. It reports only that
the process is up, never whether the docs origin is reachable. An origin
outage is exactly when the stale-serving cache is most useful, so it must not
look like an unhealthy instance.

## License

The server is [MIT licensed](LICENSE). Copied GitHub documentation content in
the test fixtures retains its upstream CC BY 4.0 license; see
[third-party notices](THIRD_PARTY_NOTICES.md) for sources, modifications and scope.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for local development, testing, dependency maintenance and release procedures.

## Verify a public release

Requires GitHub CLI, Cosign 3, and a SHA-256 utility. Run in an empty directory. Set `tag` to the release
you intend to install. Verify the checksum signature before trusting its
contents, then verify the archive and its build provenance:

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
Stop if any verification fails. Verify the archive before extracting it;
the archive attestation does not apply directly to the extracted executable.

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
