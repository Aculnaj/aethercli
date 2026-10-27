#!/bin/sh
# Offline binary/packaging smoke test. Never reads a real keychain or config.
set -eu
cd "$(dirname "$0")/.."
smoke_dir="$(mktemp -d)"
trap 'rm -rf "$smoke_dir"' EXIT INT TERM
GO="${GO:-go}"
"$GO" build -ldflags '-X github.com/Aculnaj/aethercli/internal/commands.Version=smoke-test' -o "$smoke_dir/aethercli" ./cmd/aethercli
export XDG_CONFIG_HOME="$smoke_dir/config"
export AETHER_API_KEY=sk-aetherapi-fake-offline-test
export AETHER_NO_UPDATE_CHECK=1
cli="$smoke_dir/aethercli"
"$cli" --help --json > "$smoke_dir/help.json"
"$cli" help api --json > "$smoke_dir/api-help.json"
"$cli" --version --json > "$smoke_dir/version.json"
"$cli" --config "$smoke_dir/aethercli.json" config set default-model fixture-model --dry-run --json > "$smoke_dir/preview.json"
test ! -e "$smoke_dir/aethercli.json"
"$cli" --config "$smoke_dir/aethercli.json" config set default-model fixture-model --json > "$smoke_dir/config.json"
"$cli" --config "$smoke_dir/aethercli.json" config validate --json > "$smoke_dir/validate.json"
"$cli" --config "$smoke_dir/aethercli.json" doctor --offline --json > "$smoke_dir/doctor.json"
set +e
"$cli" no-such-command --json > "$smoke_dir/error-out" 2> "$smoke_dir/error.json"
code="$?"
set -e
test "$code" -eq 2
test ! -s "$smoke_dir/error-out"
python3 - "$smoke_dir" <<'PY'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
load = lambda name: json.loads((root / (name + '.json')).read_text())
assert load('help')['schema_version'] == 1
assert 'api' in [item['name'] for item in load('help')['commands']]
assert 'request' in [item['name'] for item in load('api-help')['flags']]
assert load('version')['version'] == 'smoke-test'
assert load('preview')['dry_run'] is True
assert load('validate')['valid'] is True
assert load('doctor')['ok'] is True
assert load('error')['error']['exit_code'] == 2
assert not list((root / 'config').glob('**/*'))
for path in pathlib.Path('examples').glob('*.json'):
    assert json.loads(path.read_text())['model'] == 'MODEL_ID'
print('Offline aethercli binary, JSON help, config preview and packaging examples passed.')
PY
