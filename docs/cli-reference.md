# aethercli CLI reference

This is the command reference for the standalone Go client
[`github.com/Aculnaj/aethercli`](https://github.com/Aculnaj/aethercli), executable
`aethercli`. AetherAPI is the backend service. Aether Connect and its Node
launcher are separate products: restricted Connect tokens are not accepted here.

[README](../README.md) · [Raw reference](https://raw.githubusercontent.com/Aculnaj/aethercli/main/docs/cli-reference.md)
· [Discovery index](../llms.txt) · [Retrieval audit](discoverability-audit.md)

This reference describes this checkout. Build it with
`go build -o /tmp/aethercli ./cmd/aethercli` if the installed release is older.
Commands below use `aethercli` from PATH. `MODEL_ID`, `SESSION_ID` and request
filenames are substitution points, not promised catalog entries.

## Installation and identity

```sh
go install github.com/Aculnaj/aethercli/cmd/aethercli@latest
aethercli --version
aethercli --version --json
```

Go 1.24.2+ is required for source builds. Release executables are standalone
binaries named `aethercli` (`aethercli.exe` on Windows). Release archives are
`aethercli_<os>_<arch>.tar.gz` (Windows: `.zip`), with os `linux`, `darwin` or
`windows`, arch `amd64` or `arm64`. The existing shell installer supports Linux
and macOS. `AETHER_INSTALL_DIR` selects its destination; `AETHER_VERSION` selects
a release tag. The installer and `update` replace only the CLI binary.

## Machine-readable help

```sh
aethercli --help --json
aethercli ask --help --json
aethercli help api --json
aethercli config set --help --json
```

Every command's normal `--help` remains available. JSON help is generated from
Cobra's live command tree and includes:

- `schema_version`: currently `1`.
- `name`, `usage`, `summary`, `description`, `example`.
- `flags`: name, type, default, description, optional shorthand; includes inherited flags.
- `commands`: immediate child names, summaries and usage.
- `exit_codes`: the process exit contract.

Read a child's help for its flags and argument syntax. Help is offline and does
not consult config, credentials or update servers. `--version --json` returns
`{"version":"…"}`.

## Global flags, environment and unattended execution

- `--json`: JSON data on stdout and JSON errors on stderr. Disables interaction.
- `--non-interactive`: disables prompts, TUI, spinners and automatic update checks.
- `--config PATH`: selects only aethercli's non-secret JSON config file.
- `--help`, `-h`: command help.
- `--version`, `-v`: root version (Cobra's standard version flag).

Both terminal stdin and terminal stdout are required for implicit interaction.
A pipe, redirected output, `--json` or `--non-interactive` makes the invocation
headless. Missing input, model or credentials then produces an error; setup/TUI
never starts as an implicit recovery action. The CLI never opens a browser.

`AETHER_API_KEY` takes precedence over the OS keychain. Inject a normal
`sk-aetherapi-…` key through a secret manager. Keychain service `aether`, account
`api-key`, and config directory `aether/config.json` retain their historical
names. Config is under `os.UserConfigDir()` (for example
`$XDG_CONFIG_HOME/aether/config.json` on Linux). No API key is written there.
`AETHER_NO_UPDATE_CHECK` disables automatic update hints even in a terminal.

An OS may independently require keychain unlock/access approval. For unattended
execution that must avoid OS dialogs, supply `AETHER_API_KEY`; CLI prompt
suppression cannot override OS policies. Public model discovery does not access
the keychain or send a credential.

## Errors and exit codes

Results go to stdout, diagnostics/errors to stderr. With `--json`, one error
envelope is written on stderr; stdout remains empty for failures before output:

```json
{
  "error": {
    "code": "rate_limit_exceeded",
    "message": "rate limit exceeded: wait and retry the request",
    "exit_code": 4,
    "http_status": 429,
    "retry_after": "1",
    "request_id": "req-example"
  }
}
```

`http_status`, `retry_after`, `request_id` and `response_status` are optional.
`response_status` (`failed`, `incomplete`, `cancelled`) marks a terminal
Responses status that arrived with HTTP 2xx; see the `api` section. They are supplied when
available from an HTTP error response; retry-after preserves the backend header
as a string (seconds or HTTP date). Errors discovered inside a successful HTTP
stream have their server code, or `stream_error`; they do not claim a different
HTTP response status. Do not parse human messages. Unknown backend error codes
are preserved. Known credential patterns are redacted from diagnostics; do not
put other secrets into prompts, arguments or backend error messages.

- `0`: success.
- `1`: local operation or file error.
- `2`: invalid command/arguments/configuration, or input requiring a terminal.
- `3`: credential unavailable/invalid or HTTP authentication/permission failure.
- `4`: rate limited.
- `5`: network failure or timeout (including HTTP 408/504).
- `6`: other backend, malformed response, protocol or stream failure.
- `7`: `status`/`doctor` reported failed checks.
- `130`: canceled operation (Ctrl-C).

Local codes include `invalid_arguments`, `invalid_config`,
`credentials_unavailable`, `file_not_found`, `network_error`, `timeout`,
`diagnostic_failed`, `invalid_response`, `invalid_stream`, `stream_truncated`,
`response_too_large`, `sandbox_unverified`, `sandbox_base_url`, `canceled`,
and for Responses without a server code `response.failed`,
`response.incomplete` and `response.cancelled`.
A backend error without a code uses `api_error`.

`doctor`/`status` intentionally emit a complete report on stdout before returning
7 and a `diagnostic_failed` error on stderr. Streaming can leave partial output
on stdout before failure. Always check the exit status. No inference request is
automatically retried: callers must decide whether a retry could duplicate work
or charges. A 429 header is advice, not a promise that retrying will succeed.

## status and doctor

```sh
aethercli status --json
aethercli status --offline --json
aethercli doctor --timeout 5s --json
```

Both commands perform the same safe checks and return `schema_version: 1`, `ok`,
`config_path`, `credential_source`, and a `checks` array. Check statuses are
`ok`, `warning`, `error` or `skipped`; each includes detail and an optional
concrete hint.

Checks cover config syntax/URL validity, whether a default model is set,
environment presence, keychain availability when selected, normal-key format,
backend reachability via anonymous `GET /v1/models`, and credential acceptance
via authenticated `GET /v1/usage`. A public catalog response alone is never
reported as proof of authentication. Only GET requests are made. No provider
probe, installation, config write or budget change is performed.

`--offline` skips both network checks and explicitly leaves authentication
unverified. Missing config/default model is a warning because explicit model
selection is valid. Missing credentials, invalid config, failed backend access
or failed authentication make `ok` false and exit 7. Invalid config prevents
network access entirely. With an environment key, keychain access is skipped;
its independent health is then unknown. `--timeout` bounds all online checks
together, default 5s. Keys and usage response values are not printed.

## models

```sh
aethercli models --json
aethercli models list --json
aethercli models list --all --json
aethercli models list --provider OpenAI --search gpt --json
aethercli models list --endpoint responses --json
aethercli models list --endpoint chat/completions --capability tool_calling --json
aethercli models list --endpoint responses --capability structured_outputs.json_schema --json
aethercli models show MODEL_ID --json
```

`models` and `models list` call public `GET /v1/models`, without credentials.
The legacy `models --json` output remains an array of models declaring Chat
Completions support. It is not wrapped in a new envelope. `--all` includes all
catalog entries. `--endpoint PATH` selects models explicitly declaring that
endpoint in `endpoint`, `supported_endpoints` or as a key of the `capabilities`
map (for example `/v1/responses/compact`), and also allows non-chat entries;
paths accept `responses`, `/responses`, `v1/responses` or `/v1/responses`. URLs
are rejected with exit 2. `--provider` matches a provider exactly, ignoring case;
`--search` is a case-insensitive model-ID substring. Filters combine with AND.

`--capability FEATURE` follows the API's versioned catalog contract
(`capabilities_version: 1`): capabilities are declared per public endpoint as
`capabilities["/v1/<endpoint>"].<feature>` with the string values `supported`,
`unsupported` or `unknown`. The filter therefore **requires `--endpoint`**
(otherwise exit 2, `invalid_arguments`) and keeps a model only if the value for
exactly that endpoint is the string `supported`. `unsupported`, `unknown`, a
missing endpoint or feature, `null`, malformed values, another
`capabilities_version` and legacy model-wide booleans such as
`capabilities.tool_calling: true` never match. One endpoint's declaration is
never reused for another (for example Chat Completions for Responses). The flag
is repeatable; all features must be supported (AND).

FEATURE is a dotted path to a tri-state value, for example `tool_calling`,
`parallel_tool_calls`, `structured_outputs.json_schema`,
`modalities.input.image`, `modalities.output.text` or `reasoning.supported`.
Future tri-state features can be named the same way. Groups
(`structured_outputs`, `modalities`, `modalities.input`, `reasoning`), numeric
limits (`max_output_tokens`, `output_token_limit`), `reasoning.options` and
`access` are rejected with exit 2; inspect them with `models show MODEL_ID
--json`. `models` uses the public catalog without credentials, so the
authenticated `access` advice is not present there. If the server publishes no
capabilities yet, the filter returns an empty array. No model name, provider or
price heuristics are used. `models show` searches the entire catalog, returns
one model, and does not apply list filters.

Text listing shows ID, provider, context and the catalog's price string. `show`
prints declared metadata and a short explanation. JSON retains legacy fields
and all unknown catalog fields, including numeric prices, optional capability
objects, discounts and context pricing. Prices are API metadata, not proof of
what a specific request will cost. `models show` returns exit 2 with
`model_not_found` if the ID is absent.

## usage and budget

```sh
aethercli usage --json
aethercli usage --period today
aethercli usage --period 7 --json
aethercli usage --period 30 --json
aethercli usage --period retained --json
aethercli budget --json
```

`usage` makes authenticated `GET /v1/usage?period=…`; period defaults to `30`.
`budget` makes authenticated `GET /v1/budget-alerts`. Both have `--timeout`
(default 10s). JSON passes through the backend response without fabricated
fields or fallback figures. Text is a sorted path/value view, preserving
account/key/report namespaces, units, period coverage, previous-period and
forecast fields; null is displayed as `unavailable`, never zero.

Budget alerts are advisory state. A backend-provided forecast remains a
forecast; the CLI does not create one. Visibility follows the normal key's
project/key permissions. These commands never call `/connect/usage`, change
budgets, purchase credits, or send an inference request. A server lacking the
endpoint returns an ordinary backend error; the CLI does not substitute an
estimate.

## api: explicit request files and stdin

The following requests can incur provider charges. Select a suitable current
model from `models list --all`, then inspect its declared endpoints.

```sh
# jq is used only to prepare these example request files.
MODEL_ID='replace-with-a-model-from-the-catalog'
jq --arg model "$MODEL_ID" '.model = $model' examples/chat.json > request.json
aethercli api --protocol chat --request request.json --json
```

Exactly one selector is required:

- `--protocol chat` → `/v1/chat/completions`.
- `--protocol responses` → `/v1/responses`.
- `--protocol messages` → `/v1/messages`.
- `--endpoint PATH` accepts those three paths plus `embeddings` and
  `images/generations` (also accepts `/…` and `/v1/…` forms).

`--request FILE` is required; `--request -` reads stdin. A request is one JSON
object, limited to 32 MiB. Model, messages/input, tool schemas and other fields
are caller supplied and forwarded without narrowing them to `ask`'s text schema.
Unknown request fields are left for the selected backend to validate. No SDK,
Connect flow, connector config, URL forwarding, path traversal or automatic
endpoint choice is involved. Binary/audio/multipart endpoints are outside this
JSON command's scope.

```sh
jq --arg model "$MODEL_ID" '.model = $model' examples/responses.json |
  aethercli api --protocol responses --request - --stream
jq --arg model "$MODEL_ID" '.model = $model' examples/messages.json |
  aethercli api --endpoint messages --request - --json
```

Choose a model appropriate to each protocol separately. `--stream` requests SSE;
a boolean `stream` in the request is honored unless the flag is explicitly set.
`--stream=false` overrides a request's `stream: true`. Only the three text
protocols accept streaming. Successful non-streaming responses must be valid
JSON and are limited to 32 MiB; JSON is passed through, not wrapped.

Non-streamed Responses (`--protocol responses`, `--endpoint responses` and the
Responses sandbox) are additionally checked for a terminal status delivered with
HTTP 2xx, matching the stream events `response.failed`/`response.incomplete`:

- `status: "failed"` → error code from `error.code` (else `error.type`, else
  `response.failed`); the message includes the response ID and `error.message`.
- `status: "incomplete"` → `response.incomplete`, with `incomplete_details.reason`.
- `status: "cancelled"` → `response.cancelled`.

These exit 6 (or 4 for `rate_limit_exceeded`), set `response_status` in the JSON
error envelope, carry no `http_status`, and write **nothing to stdout**: stdout
holds only successful results. `completed`, and `queued`/`in_progress` from an
explicitly requested background response, are passed through with exit 0. Chat
Completions, Messages, embeddings and image JSON bodies are not reinterpreted;
their failures are HTTP errors.
`--timeout` defaults to 60s and bounds the whole operation, including a stream.

Streaming stdout is SSE, including when `--json` is set. Events retain event
names and JSON payloads; newline framing is normalized. Failure events are
reported on stderr and not forwarded as successful result events. The parser
recognizes Chat `[DONE]`, Responses `response.completed`, and Messages
`message_stop`. It reports server failure/incomplete events and missing terminal
markers, including after partial output. Events/lines are limited to 1 MiB.
Heartbeats without data are consumed. The CLI flushes each delivered event.

## sandbox

```sh
aethercli sandbox --scenario success --protocol chat --json
aethercli sandbox --scenario success --protocol responses --stream
aethercli sandbox --scenario success --protocol messages --json
aethercli sandbox --scenario rate_limit --json
aethercli sandbox --scenario timeout --delay-ms 250 --json
aethercli sandbox --scenario provider_unavailable --json
aethercli sandbox --scenario stream_error --stream --json
```

The normal API key is used. The command supplies `model: aether-sandbox` and a
small fixed text payload. Only these three exact routes are reachable:
`/v1/sandbox/chat/completions`, `/v1/sandbox/responses`, `/v1/sandbox/messages`.
The configured base URL must end exactly in `/v1`. No redirect or error can
switch the request to a production provider route. Successful responses must
confirm `X-Aether-Sandbox: true`; otherwise `sandbox_unverified` is returned.

The server must enable `AETHER_SANDBOX_ENABLED`. Setting it in the CLI's local
environment does not enable a remote sandbox. `sandbox_disabled`, auth and
other errors are returned as-is without any provider fallback. This follows
the referenced backend contract; the CLI cannot verify a different server's
internal implementation or grant it sandbox support.

`--protocol` defaults to `chat`; `--scenario` defaults to `success`.
`--delay-ms` is 0..5000 (default 250). `--timeout` is the client deadline,
default 10s. A simulated `timeout` is an HTTP 504; a shorter client deadline can
instead exercise client cancellation. `stream_error` requires `--stream`.
Simulated rate-limit, timeout, provider-unavailable and stream-error results
intentionally exit 4, 5, 6 and 6 respectively. They are tests of error handling,
not successful inference results. Sandbox history records may be created by
the backend; these simulated requests do not become real provider calls.

## config

```sh
aethercli config get --json
aethercli config get base-url --json
aethercli config get default-model
aethercli config validate --json
aethercli config set default-model MODEL_ID --dry-run --json
aethercli config set base-url https://api.aetherapi.dev/v1 --dry-run --json
aethercli config set default-model MODEL_ID --json
aethercli config clear default-model --dry-run --json
aethercli config clear key --dry-run --json
aethercli config clear --dry-run --json
```

`get` with no key retains the existing JSON config output, even without
`--json`; a single-key `--json` returns an object keyed by the requested CLI key.
`validate` checks structural validity, safe URL transport and credential-free
model settings. Config operations make no network requests or update checks.

`set` accepts `default-model` or `base-url`. Empty values are rejected. The base
URL must be HTTPS, or HTTP on localhost/loopback for testing, without embedded
credentials, query strings, fragments, encoded paths or traversal. An invalid
stored base URL can be repaired with `config set base-url …`.

`set` and `clear` support `--dry-run` and return `before`, `after`, `path` and
`dry_run` when previewing or with `--json`. A preview never writes a file or
changes keychain contents. `clear key` deletes only the aethercli keychain
entry; it does not unset environment variables. `clear default-model` removes
the default model; `clear base-url` restores the built-in URL. `clear` without
a key deletes this CLI's config and keychain entry. Use the explicit preview
before clearing if unsure. Mutations do not require prompts or extra confirmation.

Writes use a private (0600) temporary file and atomic rename. Existing unknown
aethercli fields are preserved during writes. Unrelated JSON tool schemas and
symlink destinations are refused; other apps' connector files are not edited.
`get` exposes only known non-secret config fields. `--config PATH` selects a
separate config file but uses the same OS credential entry. Sessions live next
to that config file.

## ask, estimates, chat and sessions

```sh
aethercli ask "Hello" --model MODEL_ID --json
printf '%s\n' 'Explain DNS' | aethercli ask --model MODEL_ID --non-interactive
aethercli ask "Review this file" --file internal/api/api.go --model MODEL_ID
aethercli ask "Review the project" --context . --model MODEL_ID
aethercli ask "Write a release note" --stream --model MODEL_ID
aethercli ask "Estimate this" --estimate --max-tokens 1000 --model MODEL_ID --json
```

`ask` uses an explicit `--model`, otherwise the configured default. An explicit
model works with no saved default. Prompt arguments take precedence over piped
stdin; without either, a prompt is requested only in an interactive terminal.
`--temperature` and `--max-tokens` are sent only when supplied. `--file`, `-f`,
and `--context` can be repeated. Existing context collection remains available.

Non-streaming `ask --json` preserves `{"model":"…","content":"…"}`.
`ask --stream` writes text deltas, not SSE or JSON; combining it with `--json`
is rejected. Use `api --stream --json` for protocol events and structured errors.
`ask` streaming keeps the existing response-header timeout without a whole-stream
limit; use `api --timeout` when a bounded total streaming deadline is required.
Ctrl-C cancels the request.

`--estimate` fetches the catalog without sending a provider request. Tokens are
a UTF-8 byte-count/4 heuristic. The cost calculation understands a catalog
string with USD input/output prices per 1M tokens and assumes the requested
`--max-tokens` is the output allowance. It is not a tokenizer, cache prediction,
tier-aware billing engine or quote. If price parsing or an output allowance is
unavailable, JSON uses `estimated_cost_usd: null`; text explains why. Values
are labeled `estimated`, never mixed into usage. Context tiers and other
request-dependent pricing may change the actual bill.

```sh
aethercli setup
aethercli chat
aethercli tui
aethercli chat "Start a conversation" --model MODEL_ID --json
aethercli chat --resume "Continue" --model MODEL_ID --json
aethercli chat --session SESSION_ID "Next question" --model MODEL_ID --json
aethercli sessions list --json
aethercli sessions show SESSION_ID --json
```

`setup`, the root without arguments, `tui` and `chat` without a supplied prompt
require interactive input/output terminals. Setup hides key input on a terminal
and validates it before storing it in the OS keychain. Chat with a prompt can
run headlessly; JSON returns the same model/content response as `ask` and still
persists the conversation. `sessions list --json` returns the summary array;
`show` preserves the stored JSON session shape. Use IDs from the list.
Conversations contain prompt/assistant text and may be sensitive.

## update

```sh
aethercli update --json
aethercli update --install-dir /tmp/aethercli-bin --json
```

An explicit update checks GitHub and installs the latest binary. JSON reports
`updated` and `version`, and `path` after installation. This is a deliberate
network/filesystem operation. Release builds may check once daily for a newer
version during interactive root/ask/chat/TUI commands and save check timestamps;
headless, JSON, help, diagnostics and config operations skip automatic checks.
No automatic installation is performed.

## Backend dependencies and validation scope

This CLI targets the existing `/v1/models`, `/v1/usage`, `/v1/budget-alerts`,
text-protocol routes, and explicitly enabled `/v1/sandbox/*` contract. Optional
future catalog fields pass through. Missing capabilities, endpoints, budget
visibility or sandbox support cannot be manufactured by a client release.

`go test ./...` exercises existing features and new automation regressions using
local fake HTTP servers and secret stores. `sh scripts/smoke.sh` builds a release
version binary and checks offline examples/JSON/errors in isolated temporary
config directories. It uses Python 3 for JSON assertions. Neither validation
requires a real API key or paid provider call. Cross-builds validate compilation,
not native keychain behavior on every target OS. See the public retrieval audit
for what was actually observable without login or JavaScript.
