# Reviewed release notes

Write each paragraph and list item on one source line. Let GitHub wrap text to fit the reader's screen; do not insert manual line breaks at a fixed width.

Add `<tag>.md` in this directory before tagging a release. The filename must match the tag exactly, including `v` and any prerelease suffix. The publishing task passes that file to GoReleaser as the complete release notes, replacing the automatic commit list. GoReleaser appends the container reference and download information in either mode.

The Docker Manifests section lists the versioned image as `tag@sha256:digest`, using the published multi-platform manifest digest. Stable releases also list `latest` with its published digest; prereleases omit it. Do not write or copy these digests into reviewed notes: GoReleaser supplies them after pushing the images.

Start with `## Summary of Changes`. Group user-facing entries under headings such as `### Features`, `### Bugfixes` and `### Breaking Changes`. Put dependency, CI and other maintenance changes in separate sections after user-facing changes. Omit empty sections. Add a short introduction only when it helps explain the release.

Each bullet should describe the observable change and link to its PR. Explain the failure that was fixed, rather than the implementation used to fix it. Include required upgrade steps for breaking changes. Omit commit hashes and Conventional Commit prefixes. Verify references and credit contributors when appropriate; do not invent author names or links.

For example, the wording for a prerelease-classification fix could be:

```markdown
## Summary of Changes

### Bugfixes

* Prereleases no longer replace the latest stable GitHub release. Users following the latest release continue to receive the stable version.
```

Add the actual PR reference to the bullet when preparing the release. Review the complete file against the changes since the previous release. Do not repeat the automatic commit list beneath it.

A release without a matching file uses the automatically grouped changelog. An empty matching file stops publication. Notes for other tags are ignored; there is no shared summary to clear after publishing.
