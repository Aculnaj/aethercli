# Public documentation retrieval audit

Checked on **2026-10-27**, approximately **17:00–17:08 UTC**, from this development
environment. This is a bounded retrieval/search check, not a crawler/indexing
certification. No login, browser automation or JavaScript execution was used.
Only public documentation/repository pages were fetched; no authenticated API
or paid provider requests were made.

## Direct public retrieval

Python `urllib.request` performed ordinary GET requests with
`User-Agent: aethercli-docs-audit/1.0`, without cookies or credentials.
HTML checks removed script/style content before inspecting visible text.

- [GitHub repository](https://github.com/Aculnaj/aethercli): HTTP 200,
  `text/html; charset=utf-8`; public repository identity and README were readable
  without login. The search tool's direct page reader also exposed the README.
- [Raw README](https://raw.githubusercontent.com/Aculnaj/aethercli/main/README.md):
  HTTP 200, `text/plain; charset=utf-8`, 3,097 decoded characters at check time.
  The live response used `aethercli` and the current Go install package, without
  the obsolete `cmd/aether@…` path.
- [Raw installer](https://raw.githubusercontent.com/Aculnaj/aethercli/main/install.sh):
  HTTP 200, `text/plain; charset=utf-8`, 1,433 decoded characters; used the
  `aethercli` binary/archive name.
- [New raw CLI reference](https://raw.githubusercontent.com/Aculnaj/aethercli/main/docs/cli-reference.md):
  HTTP 404 at check time. It is added in this implementation branch and has not
  been merged/published to `main`.
- [Repository raw llms.txt](https://raw.githubusercontent.com/Aculnaj/aethercli/main/llms.txt):
  HTTP 404 at check time, for the same publication reason. The new file is an
  optional link index, not a hosting feature.
- [API docs root](https://aetherapi.dev/docs) and
  [API quickstart](https://aetherapi.dev/docs/quickstart): HTTP 200,
  `text/html; charset=utf-8` with the audit user agent. Both supplied readable
  Quickstart text, base URL/auth guidance and documentation links in the HTML
  response itself. Each page contained five script tags; the inspected text
  did not require executing them.
- [API site's llms.txt](https://aetherapi.dev/llms.txt): HTTP 200,
  `text/plain; charset=utf-8`, 3,553 decoded characters at check time. It did
  not mention `aethercli`. This external website belongs to the API project;
  no changes to it were made here.

**Access varies by client.** A repeat request using Python's default user agent
received HTTP 403 for the API docs root, quickstart and site `llms.txt`. A repeat
with `aethercli-docs-audit/1.0` again received 200 for all three. The search tool's
direct `/docs` reader also reported that URL as inaccessible even though the
explicit HTTP audit succeeded. These results demonstrate a real retrieval
boundary; they do not establish which individual crawlers are allowed.

**Cached readers can disagree with live HTTP.** The search tool's direct raw
README reader displayed an older cached revision using `aether` and
`cmd/aether`, while the live anonymous HTTP GET and GitHub-rendered README used
`aethercli`. Public accessibility does not guarantee that a search/AI system
has refreshed its copy.

## Search observation

The available web search was queried with:

```text
site:github.com/Aculnaj/aethercli aethercli
aethercli AetherAPI CLI Go
```

No identifiable result for this standalone Go repository was returned in the
bounded result set. Results instead included the official
[API quickstart](https://aetherapi.dev/docs/quickstart), API/dashboard pages,
and several different products named Aether. A Node Connect launcher also
appeared among broader results. This supports using the exact `aethercli`
executable, `github.com/Aculnaj/aethercli` repository/package and clear product
identity in the README and reference. It does **not** prove that the repository
is absent from every search index or explain a search provider's ranking.

## Changes made in this repository

The README now links a complete Markdown command reference, raw-text URLs,
JSON help, request templates and this audit. Names match `go.mod`, executable,
release archive and installer. The reference explains the preserved keychain
storage names, normal-key/Connect separation, headless/JSON/exit contracts,
actual usage/budget endpoints, optional capability metadata, streaming and
sandbox requirements. Example files and the offline smoke script accompany the
reference. GoReleaser includes these text resources in archives.

The executable's `--help --json` is generated from its live command tree;
`schema_version: 1` makes its format explicit. Agents can read local help and
Markdown without needing a web search or JavaScript application.

## Limits and follow-up after publication

The new raw links cannot return this branch's content until the changes land
on `main`. No merge, release, push, web deployment, sitemap/robots change,
search-console submission or external documentation edit was performed. This
Go project does not acquire a documentation website through `llms.txt`.

After publication, repeat anonymous GETs to the exact reference/index URLs and
check that the response contains the new command names. Repeat search queries
separately: successful fetches and `llms.txt` links do not guarantee indexing,
ranking, refresh time or inclusion in an AI answer. No claims are made about
private search indexes or individual AI providers' crawler behavior.
