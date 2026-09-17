# Changelog

## Unreleased

Nothing has been released yet, so this section describes the current shape of
the server rather than a diff against a published version.

- `list_docs`, `search_docs` and `get_doc` MCP tools over stdio and streamable
  HTTP, stale-servable TTL cache with byte-capped LRU, outbound rate limiting
  and retry with `Retry-After` support, optional disk cache, hardened Docker
  image.
- Built on the official `modelcontextprotocol/go-sdk` (MCP 2026-07-28, with
  fallback to 2025-11-25 and earlier). Tool input schemas are inferred from Go
  structs, so required arguments are validated before a handler runs; a
  receiving middleware turns a handler panic into a JSON-RPC error rather than
  a dead transport.
- Aligned with the 2026-07-28 changes that apply to a read-only tools server:
  `tools/list` carries the now-required `ttlMs`/`cacheScope` cache hints
  (SEP-2549) in a deterministic order, and the deprecated `logging` capability
  (SEP-2577) is no longer advertised; the SDK would otherwise send it by
  default. The stateless core needs no work here beyond the HTTP transport
  already running session-free.
- Origin is `docs.github.com`. The catalogue merges the curated `llms.txt`
  shortlist (titles and descriptions) with the Page List API (the
  authoritative set of paths that resolve), so `get_doc` accepts the thousands
  of slugs GitHub's own pages cross-link to, not just the curated ~120.
- `search_docs` delegates to the origin's `/api/search/v1` endpoint, which
  indexes every page body server-side: this origin publishes no bundled
  full-text file, so that is the only way search reaches pages nobody has
  fetched. An origin outage degrades it to local scoring over catalogue
  metadata plus cached bodies rather than failing.
- Pages are fetched from the `.md` form of a path; the bare path serves
  rendered HTML. The HTTP client refuses an HTML body on a `200` outright and
  does not retry it, so an interstitial or error page can never be cached as
  documentation.
- `get_doc` accepts a bare slug, an absolute path, a full docs URL, a trailing
  `.md` or a `#anchor`, matches headings by text or kebab-anchor, and lists the
  page's real headings on a miss.
- `get_doc` `query` parameter: returns only the page sections matching the
  keywords, verbatim with heading breadcrumbs: a large token saving for
  focused lookups, with no external calls or credentials.
