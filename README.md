# Aether CLI

`aethercli` is a small Go CLI for AetherAPI text models. It stores your AetherAPI key once, lets you choose a default model, and then sends prompts from your terminal.

## Install

From GitHub Releases:

```sh
curl -fsSL https://raw.githubusercontent.com/Aculnaj/aethercli/main/install.sh | sh
```

The installer places `aethercli` in `~/.local/bin` by default. Override with:

```sh
AETHER_INSTALL_DIR=/usr/local/bin sh install.sh
```

From source:

```sh
go install github.com/Aculnaj/aethercli/cmd/aethercli@latest
```

## First Run

Run setup once:

```sh
aethercli setup
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
aethercli ask "Write a short haiku about terminals" --model claude-sonnet-4-6
```

Ask with your saved default model:

```sh
aethercli ask "Explain DNS in one paragraph"
```

Pipe a longer prompt:

```sh
cat prompt.txt | aethercli ask --model gpt-4o
```

Add project context from files or directories:

```sh
aethercli ask "Review this implementation" --file internal/api/api.go
aethercli ask "Find risks in this project" --context .
```

Directory context skips common build folders and honors simple `.gitignore`
patterns.

Start interactive mode:

```sh
aethercli
```

Start the Bubble Tea chat TUI:

```sh
aethercli chat
aethercli tui
```

Inside the TUI, type `/help` for commands such as `/models`, `/sessions`,
`/resume <id>`, `/new`, `/clear`, and `/quit`.

Send or resume a saved chat session without opening the TUI:

```sh
aethercli chat "Help me debug this failing test"
aethercli chat --resume "Continue with a smaller fix"
aethercli sessions list
aethercli sessions show 20260612-103000
```

Stream output live:

```sh
aethercli ask "Draft a release note" --stream
```

Print JSON:

```sh
aethercli ask "Summarize this in one sentence" --json
```

Preview estimated tokens and cost before sending a request:

```sh
aethercli ask "Explain this diff" --estimate --max-tokens 1000
```

List text/chat models:

```sh
aethercli models
aethercli models --json
```

Update the CLI when a new GitHub release is available:

```sh
aethercli update
```

Release builds also check for updates automatically at most once per day and print a hint when a newer version exists.

Manage config:

```sh
aethercli config get
aethercli config set default-model claude-sonnet-4-6
aethercli config get default-model
aethercli config clear key
aethercli config clear
```

## Development

Run tests:

```sh
go test ./...
```

Build locally:

```sh
go build ./cmd/aethercli
```

Create release artifacts locally if GoReleaser is installed:

```sh
goreleaser release --snapshot --clean
```
