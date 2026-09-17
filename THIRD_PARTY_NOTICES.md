# Third-party notices

The server implementation is licensed under the [MIT License](LICENSE).
The documentation fixtures listed below contain material from GitHub Docs,
copyright GitHub, Inc. and contributors. GitHub licenses its documentation
and content in the upstream assets, content and data directories under
[Creative Commons Attribution 4.0 International](licenses/github-docs-CC-BY-4.0.txt).
That license, including its disclaimer of warranties, applies to the copied
documentation rather than the repository's MIT license.

The catalogue and search fixtures include documentation titles, descriptions
and excerpts. This notice attributes that content; it does not assert that
every generated API field or page path has a separately confirmed CC BY licence.

## GitHub Docs fixtures

These files under `internal/docs/testdata/` preserve examples of the public
documentation and API formats used by the parser tests, fuzz seeds and local
fixture server:

| File | Source | Modifications |
| --- | --- | --- |
| `nodejs-guide.md` | [Building and testing Node.js](https://docs.github.com/en/actions/tutorials/build-and-test-code/nodejs) | Captured Markdown representation of the article. |
| `llms.txt` | [GitHub Docs catalogue](https://docs.github.com/llms.txt) | Captured catalogue with titles and descriptions. |
| `pagelist.txt` | [GitHub Docs Page List API](https://docs.github.com/api/pagelist/en/free-pro-team@latest) | Trimmed to catalogue paths and a sample of other paths; explanatory comments added. |
| `search.json` | [GitHub Docs Search API](https://docs.github.com/api/search/v1) | Captured search results with documentation excerpts, formatted as JSON. |

These are fixed test snapshots, not a current copy of the documentation.
The fixture server rewrites catalogue URLs to its loopback address at runtime;
it does not modify the committed files.

See the [GitHub Docs source repository](https://github.com/github/docs) and
[upstream license](https://github.com/github/docs/blob/main/LICENSE).
This project is independently maintained and is not endorsed by GitHub.
