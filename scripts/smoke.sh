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
# Built binary against a synthetic loopback server: endpoint-scoped capability
# filters, Responses terminal statuses with HTTP 200, and streaming. No real
# backend, provider or credential is involved.
python3 - "$cli" "$smoke_dir" <<'PY'
import http.server, json, os, subprocess, sys, threading
cli, root = sys.argv[1], sys.argv[2]
caps = {'/v1/chat/completions': {'tool_calling': 'supported', 'structured_outputs': {'json_schema': 'unknown'}}, '/v1/responses': {'tool_calling': 'unsupported'}}
catalog = {'object': 'list', 'data': [
    {'id': 'fixture', 'object': 'model', 'owned_by': 'fixture', 'endpoint': '/v1/chat/completions', 'supported_endpoints': ['/v1/chat/completions', '/v1/responses'], 'capabilities_version': 1, 'capabilities': caps},
    {'id': 'fixture-unknown', 'object': 'model', 'endpoint': '/v1/chat/completions', 'capabilities_version': 1, 'capabilities': {'/v1/chat/completions': {'tool_calling': 'unknown'}}},
    {'id': 'fixture-missing', 'object': 'model', 'endpoint': '/v1/chat/completions'}]}
failed = {'id': 'resp_fixture', 'object': 'response', 'created_at': 0, 'model': 'fixture', 'status': 'failed', 'output': [], 'error': {'code': 'server_error', 'message': 'Synthetic response failure'}}
completed = dict(failed, status='completed', error=None)
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def reply(self, body, kind='application/json'):
        data = body.encode()
        self.send_response(200); self.send_header('Content-Type', kind); self.send_header('Content-Length', str(len(data))); self.end_headers(); self.wfile.write(data)
    def do_GET(self): self.reply(json.dumps(catalog))
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
        fail = request['model'] == 'fail'
        if request.get('stream'):
            terminal = 'response.failed' if fail else 'response.completed'
            event = {'type': terminal, 'response': failed if fail else completed}
            self.reply('event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"hi"}\n\nevent: %s\ndata: %s\n\n' % (terminal, json.dumps(event)), 'text/event-stream')
        else:
            self.reply(json.dumps(failed if fail else completed))
server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
config = os.path.join(root, 'fake-server.json')
with open(config, 'w') as f:
    json.dump({'base_url': 'http://127.0.0.1:%d/v1' % server.server_port}, f)
def run(*args, stdin=None):
    r = subprocess.run([cli, '--config', config, *args, '--json'], input=stdin, text=True, capture_output=True, timeout=30)
    return r.returncode, r.stdout, r.stderr
def ids(*args):
    code, out, err = run('models', 'list', *args)
    assert code == 0 and not err, (args, code, err)
    return [m['id'] for m in json.loads(out)]
assert ids('--endpoint', 'chat/completions', '--capability', 'tool_calling') == ['fixture']
assert ids('--endpoint', '/v1/chat/completions', '--capability', 'tool_calling') == ['fixture']
assert ids('--endpoint', 'responses', '--capability', 'tool_calling') == []
assert ids('--endpoint', 'chat/completions', '--capability', 'structured_outputs.json_schema') == []
code, out, err = run('models', 'list', '--capability', 'tool_calling')
assert code == 2 and not out and json.loads(err)['error']['code'] == 'invalid_arguments', (code, out, err)
code, out, err = run('api', '--protocol', 'responses', '--request', '-', stdin='{"model":"fail","input":"synthetic"}')
failure = json.loads(err)['error']
assert code == 6 and not out and failure['code'] == 'server_error' and failure['response_status'] == 'failed' and 'Synthetic response failure' in failure['message'], (code, out, err)
code, out, err = run('api', '--protocol', 'responses', '--request', '-', stdin='{"model":"ok","input":"synthetic"}')
assert code == 0 and not err and json.loads(out)['status'] == 'completed', (code, out, err)
code, out, err = run('api', '--protocol', 'responses', '--request', '-', '--stream', stdin='{"model":"ok","input":"synthetic"}')
assert code == 0 and not err and 'response.completed' in out, (code, out, err)
code, out, err = run('api', '--protocol', 'responses', '--request', '-', '--stream', stdin='{"model":"fail","input":"synthetic"}')
assert code == 6 and 'response.output_text.delta' in out and json.loads(err)['error']['code'] == 'server_error', (code, out, err)
assert 'sk-aetherapi' not in out + err
server.shutdown()
print('Binary capability filters and Responses status/exit codes passed against a loopback fake server.')
PY
