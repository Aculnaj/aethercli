# Aether CLI

`aether` is a small Go CLI for AetherAPI text models. It stores your AetherAPI key once, lets you choose a default model, and then sends prompts from your terminal.

## Install

From GitHub Releases:

```sh
curl -fsSL https://raw.githubusercontent.com/Aculnaj/aethercli/main/install.sh | sh
```

The installer places `aether` in `~/.local/bin` by default. Override with:

```sh
AETHER_INSTALL_DIR=/usr/local/bin sh install.sh
```

From source:

```sh
go install github.com/Aculnaj/aethercli/cmd/aether@latest
```

## First Run

Run setup once:

```sh
aether setup
```

The CLI asks for:

- your AetherAPI key, which must start with `sk-aetherapi-`
- your default text model, for example `claude-sonnet-4-6`

The key is stored in the OS Keychain under service `aether` and account `api-key`. Non-secret config is stored in your user config directory at `aether/config.json`.

You can also use an environment variable instead of the keychain:

```sh
export AETHER_API_KEY=sk-aetherapi-...
```

`AETHER_API_KEY` takes precedence over the stored key.

## Usage

Ask with an explicit model:

```sh
aether ask "Write a short haiku about terminals" --model claude-sonnet-4-6
```

Ask with your saved default model:

```sh
aether ask "Explain DNS in one paragraph"
```

Pipe a longer prompt:

```sh
cat prompt.txt | aether ask --model gpt-4o
```

Add project context from files or directories:

```sh
aether ask "Review this implementation" --file internal/api/api.go
aether ask "Find risks in this project" --context .
```

Directory context skips common build folders and honors simple `.gitignore`
patterns.

Start interactive mode:

```sh
aether
```

Start or resume a saved chat session:

```sh
aether chat "Help me debug this failing test"
aether chat --resume "Continue with a smaller fix"
aether sessions list
aether sessions show 20260612-103000
```

Stream output live:

```sh
aether ask "Draft a release note" --stream
```

Print JSON:

```sh
aether ask "Summarize this in one sentence" --json
```

Preview estimated tokens and cost before sending a request:

```sh
aether ask "Explain this diff" --estimate --max-tokens 1000
```

List text/chat models:

```sh
aether models
aether models --json
```

Update the CLI when a new GitHub release is available:

```sh
aether update
```

Release builds also check for updates automatically at most once per day and print a hint when a newer version exists.

Manage config:

```sh
aether config get
aether config set default-model claude-sonnet-4-6
aether config get default-model
aether config clear key
aether config clear
```

## Development

Run tests:

```sh
go test ./...
```

Build locally:

```sh
go build ./cmd/aether
```

Create release artifacts locally if GoReleaser is installed:

```sh
goreleaser release --snapshot --clean
```
