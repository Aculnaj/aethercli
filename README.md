# aethercli — Go command-line client for AetherAPI

`aethercli` is the Go CLI in [github.com/Aculnaj/aethercli](https://github.com/Aculnaj/aethercli).
It supports normal AetherAPI keys, dynamic model discovery, prompts, streaming,
cost previews, saved conversations and a terminal chat UI, plus an automation
surface for agents and CI. The executable and Go package are named `aethercli`.
Aether Connect's Node launcher is a separate product; its restricted tokens are
not credentials for this CLI.

## Documentation for people and agents

- [Complete CLI reference](docs/cli-reference.md) — commands, flags, JSON, errors, exit codes and examples.
- [Plain Markdown reference](https://raw.githubusercontent.com/Aculnaj/aethercli/main/docs/cli-reference.md).
- [Plain README](https://raw.githubusercontent.com/Aculnaj/aethercli/main/README.md).
- [Optional discovery index](llms.txt) ([plain text](https://raw.githubusercontent.com/Aculnaj/aethercli/main/llms.txt)).
- [Public retrieval audit and limits](docs/discoverability-audit.md).
- [AetherAPI documentation](https://aetherapi.dev/docs/quickstart).

The new reference/discovery files become publicly available after this change
lands on `main`. For an unmerged checkout, use its local Markdown and executable
help. This repository does not host a documentation website. `llms.txt` is a
navigation aid, not a promise of search indexing.

```sh
aethercli --help --json
aethercli models show --help --json
aethercli help api --json
```

Help is generated from the running CLI and includes commands, flags, defaults
and exit codes with `schema_version: 1`.

## Install

From GitHub Releases (Linux/macOS):

```sh
curl -fsSL https://raw.githubusercontent.com/Aculnaj/aethercli/main/install.sh | sh
```

The installer puts `aethercli` in `~/.local/bin`. To choose a directory, download
and run it with `AETHER_INSTALL_DIR`:

```sh
curl -fsSL https://raw.githubusercontent.com/Aculnaj/aethercli/main/install.sh -o install.sh
AETHER_INSTALL_DIR=/usr/local/bin sh install.sh
```

From source (Go 1.24.2 or newer):

```sh
go install github.com/Aculnaj/aethercli/cmd/aethercli@latest
```

A new command documented in this branch may require building this checkout
until a release includes it:

```sh
go build -o /tmp/aethercli ./cmd/aethercli
/tmp/aethercli --help --json
```

## Credentials and first run

In a terminal, run `aethercli setup`. The key input is hidden, and the key is
stored in the OS keychain under the existing service `aether`, account `api-key`.
Non-secret config stays at your OS user config directory's `aether/config.json`.
Those existing storage names remain compatible.

For agents and CI, inject a normal `sk-aetherapi-…` key through your environment's
secret manager as `AETHER_API_KEY`. It takes precedence over the keychain. Never
put keys in request JSON, CLI arguments, repository files or config. No browser
login is needed. OS keychain access can be subject to OS unlock/access policies;
use the environment variable when an unattended process must avoid those dialogs.

## Read-only automation

```sh
aethercli models --json
aethercli models list --all --json
aethercli models list --endpoint chat/completions --capability tool_calling --json
aethercli status --offline --json
aethercli doctor --json
aethercli usage --period 7 --json
aethercli budget --json
aethercli config get --json
aethercli config validate --json
aethercli config set default-model MODEL_ID --dry-run --json
```

Replace `MODEL_ID` with an ID returned by the live catalog. `models --json`
retains its existing array of chat models; `models list --all` includes other
endpoints. `--capability` requires `--endpoint` and matches only the catalog's
per-endpoint value `"supported"`; `unknown`, `unsupported` and missing values
never match. `doctor` and online `status` use only GET requests to `/models` and
`/usage`; neither sends a paid provider request or changes local state.
`usage` calls `/v1/usage`; `budget` calls `/v1/budget-alerts`. Backend reports,
coverage, scopes and forecast labels are retained without invented totals.

`--json` and `--non-interactive` disable interactive behavior even in a terminal.
Without a terminal, no setup, prompt, TUI, spinner or automatic update check is
started. Errors go to stderr, results to stdout. Use exit status rather than
matching human messages; [the reference](docs/cli-reference.md#errors-and-exit-codes)
defines the contract.

## Prompts, streaming and chat

The following inference examples can incur provider charges. Select a current
chat model instead of assuming any particular model is always available:

```sh
aethercli ask "Explain DNS in one paragraph" --model MODEL_ID --json
printf '%s\n' 'Explain this error' | aethercli ask --model MODEL_ID --non-interactive
aethercli ask "Review this implementation" --file internal/api/api.go --model MODEL_ID
aethercli ask "Find risks in this project" --context . --model MODEL_ID
aethercli ask "Draft a release note" --stream --model MODEL_ID
aethercli ask "Explain this diff" --estimate --max-tokens 1000 --model MODEL_ID --json
```

`--estimate` fetches only the catalog. Token and cost previews are explicitly
heuristic estimates, not billed usage or a quote. Directory context skips common
build directories and honors simple `.gitignore` patterns.

After saving a default model, retain the interactive and conversation workflows:

```sh
aethercli config set default-model MODEL_ID
aethercli
aethercli chat
aethercli tui
aethercli chat "Help me debug this failing test" --json
aethercli chat --resume "Continue with a smaller fix" --json
aethercli sessions list --json
```

Inside the TUI, `/help` lists `/models`, `/sessions`, `/resume <id>`, `/new`,
`/clear` and `/quit`. Sessions store conversation text locally; treat it as
sensitive. `aethercli sessions show SESSION_ID --json` uses an ID from the list.

## Explicit requests and sandbox

Request files in [examples](examples) are templates: replace `MODEL_ID` with a
catalog model supporting that endpoint before making a real request.

```sh
aethercli api --protocol chat --request request.json --json
cat request.json | aethercli api --endpoint responses --request - --stream
```

The endpoint or protocol is required. No automatic retries or fallback calls are
made. Streaming forwards SSE, detects server errors after partial output, and
returns a nonzero status for truncated streams. A non-streamed Responses object
with HTTP 200 but `status` `failed`, `incomplete` or `cancelled` is also an error
(exit 6, or 4 for a rate-limit code) with an empty stdout.

The explicit sandbox command always uses `/v1/sandbox/*` and `aether-sandbox`:

```sh
aethercli sandbox --scenario success --json
aethercli sandbox --scenario rate_limit --json
aethercli sandbox --scenario timeout --json
aethercli sandbox --scenario provider_unavailable --json
aethercli sandbox --scenario stream_error --stream --json
```

The server must enable `AETHER_SANDBOX_ENABLED`; failures never fall back to real
provider routes. Simulated failures intentionally return nonzero exit statuses.
See the reference for all three supported sandbox protocols and timeouts.

## Updates and configuration

`aethercli update` explicitly installs the latest GitHub release. Release builds
may print a daily update hint during interactive ask/chat/TUI use. Automation,
diagnostics, config commands and JSON/help calls skip automatic checks.

```sh
aethercli config get default-model --json
aethercli config clear key --dry-run --json
aethercli --config /tmp/aethercli-config.json config set default-model MODEL_ID --json
```

Config writes use private temporary files and atomic replacement, preserve
unknown aethercli fields, and refuse symlinks and unrelated tool schemas. The
CLI does not write connector configurations for other applications.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/aethercli
sh scripts/smoke.sh
```

Tests use local fake servers and fake secret stores, without paid provider
calls or production state. `scripts/smoke.sh` checks the built executable and
packaging conventions. GoReleaser builds `aethercli` for Linux, macOS and Windows,
on amd64/arm64; its release archives include the Markdown reference and examples.
To build release archives locally with GoReleaser installed:

```sh
goreleaser release --snapshot --clean
```
