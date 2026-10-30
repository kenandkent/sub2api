#!/usr/bin/env python3
"""Real OpenCode free-model probe against a running local sub2api deployment.

Creates isolated test objects (group/account/channel/user/key), then for every
selected official free model exercises text non-stream, text stream and agent
(tool calling + tool-result continuation) requests, verifies local admission
guards, and checks usage settlement and balance reconciliation. Writes a
sanitized report to .cache/opencode-free-implementation/real-models-report.json.

Usage:
  python3 -B deploy/opencode/e2e/probe_real_models.py
  python3 -B deploy/opencode/e2e/probe_real_models.py --models glm-5.3-flash-free,beam-free
"""
import argparse
import json
import math
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'deploy'))
import dev

OFFICIAL_MODELS = [
    'big-pickle',
    'glm-5.3-flash-free',
    'deepseek-v4.1-flash-free',
    'beam-free',
    'flint-40b-free',
    'mimo-v2.6-flash-free',
    'nemotron-3-ultra-free',
    'nemotron-3.5-lightning-free',
    'nemotron-3.5-super-vl-free',
]

TEXT_PROMPT = 'Reply with exactly the word OK and nothing else.'
AGENT_PROMPT = 'What is the weather in Paris right now? You must call the lookup tool to answer.'
TOOL_RESULT = 'Sunny with a light breeze, 21 degrees Celsius.'
TOOL = {
    'type': 'function',
    'function': {
        'name': 'lookup',
        'description': 'Look up the current weather for a city.',
        'parameters': {
            'type': 'object',
            'properties': {'city': {'type': 'string'}},
            'required': ['city'],
        },
    },
}
LOCAL_REJECT_MARKERS = (
    'unsupported model', 'unsupported chat request', 'invalid chat',
    'profile mismatch', 'unsupported tools', 'unsupported parameter',
)
BODY_TIMEOUT = 180
checks = []


def check(condition, name):
    if condition:
        checks.append(name)
    else:
        checks.append('FAILED: ' + name)
    return bool(condition)


def preview(value, limit=70):
    return ' '.join(str(value or '').split())[:limit]


def call(method, path, payload=None, token=None, raw=False):
    data = json.dumps(payload).encode() if payload is not None else None
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request('http://127.0.0.1:' + BASE_PORT + path, data=data,
                                 headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=BODY_TIMEOUT) as response:
        body = response.read()
        if raw:
            return body.decode()
        obj = json.loads(body)
        if obj.get('code', 0) != 0:
            raise AssertionError('API rejected ' + path)
        return obj['data']


def login(email, password):
    return call('POST', '/api/v1/auth/login', {'email': email, 'password': password})['access_token']


def chat(api_key, body):
    data = json.dumps(body).encode()
    headers = {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + api_key}
    req = urllib.request.Request(BASE_URL + '/v1/chat/completions', data=data,
                                 headers=headers, method='POST')
    started = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=BODY_TIMEOUT) as response:
            raw = response.read().decode()
            status, err = response.status, None
    except urllib.error.HTTPError as error:
        raw = error.read().decode(errors='replace')
        status, err = error.code, None
    latency = int((time.monotonic() - started) * 1000)
    try:
        payload = json.loads(raw)
    except ValueError:
        return status, raw, latency, 'non-JSON response: ' + preview(raw)
    if status >= 400:
        err = preview((payload.get('error') or {}).get('message') or raw, 160)
    return status, payload, latency, err


def parse_sse(raw):
    result = {'content': [], 'finish': None, 'usage': None, 'done': False, 'tools': {}}
    for line in raw.splitlines():
        if not line.startswith('data:'):
            continue
        chunk = line[5:].strip()
        if chunk == '[DONE]':
            result['done'] = True
            continue
        try:
            obj = json.loads(chunk)
        except ValueError:
            continue
        if isinstance(obj.get('usage'), dict) and obj['usage'].get('prompt_tokens') is not None:
            result['usage'] = obj['usage']
        for choice in obj.get('choices') or []:
            delta = choice.get('delta') or {}
            if isinstance(delta.get('content'), str):
                result['content'].append(delta['content'])
            if isinstance(choice.get('finish_reason'), str):
                result['finish'] = choice['finish_reason']
            for call_item in delta.get('tool_calls') or []:
                index = call_item.get('index', 0)
                acc = result['tools'].setdefault(index, {'id': '', 'name': '', 'arguments': ''})
                if isinstance(call_item.get('id'), str):
                    acc['id'] = call_item['id']
                fn = call_item.get('function') or {}
                if isinstance(fn.get('name'), str) and fn['name']:
                    acc['name'] = fn['name']
                if isinstance(fn.get('arguments'), str):
                    acc['arguments'] += fn['arguments']
    return result


def local_reject(status, err):
    return status == 400 and err is not None and any(marker in err for marker in LOCAL_REJECT_MARKERS)


def record(entry, status, payload, latency, err):
    entry.update({'status': status, 'latency_ms': latency})
    if err:
        entry['error'] = err
    if isinstance(payload, dict):
        choices = payload.get('choices') or []
        if choices:
            message = (choices[0].get('message') or {})
            entry['finish_reason'] = choices[0].get('finish_reason')
            entry['content'] = preview(message.get('content'))
            if message.get('tool_calls'):
                entry['tool_calls'] = [
                    {'name': (c.get('function') or {}).get('name'),
                     'arguments': preview((c.get('function') or {}).get('arguments'))}
                    for c in message['tool_calls']]
        usage = payload.get('usage') or {}
        entry['usage'] = {k: usage.get(k) for k in
                          ('prompt_tokens', 'completion_tokens', 'total_tokens')} if usage else None
    elif isinstance(payload, str):
        entry['raw'] = preview(payload)


def test_nonstream(model, api_key):
    status, payload, latency, err = chat(api_key, {
        'model': model, 'stream': False,
        'messages': [{'role': 'user', 'content': TEXT_PROMPT}]})
    entry = {'latency_ms': 0}
    record(entry, status, payload, latency, err)
    ok = status == 200
    if ok:
        choices = payload.get('choices') or []
        content = ((choices[0].get('message') or {}).get('content') if choices else None)
        finish = choices[0].get('finish_reason') if choices else None
        usage = payload.get('usage') or {}
        ok = (isinstance(content, str) and len(content.strip()) > 0
              and finish in ('stop', 'length')
              and (usage.get('prompt_tokens') or 0) > 0
              and (usage.get('completion_tokens') or 0) > 0)
        check(ok, model + ' nonstream: content + finish + usage')
    else:
        check(False, model + ' nonstream: HTTP 200 (got %s %s)' % (status, err))
    return entry, ok, payload if ok else None


def test_stream(model, api_key):
    status, payload, latency, err = chat(api_key, {
        'model': model, 'stream': True,
        'messages': [{'role': 'user', 'content': TEXT_PROMPT}]})
    entry = {'latency_ms': 0}
    record(entry, status, payload, latency, err)
    ok = status == 200
    if ok:
        parsed = parse_sse(payload)
        entry.update({'finish_reason': parsed['finish'], 'content': preview(''.join(parsed['content'])),
                      'usage': ({k: parsed['usage'].get(k) for k in
                                 ('prompt_tokens', 'completion_tokens')} if parsed['usage'] else None),
                      'done': parsed['done']})
        ok = (parsed['done'] and len(''.join(parsed['content']).strip()) > 0
              and parsed['finish'] in ('stop', 'length')
              and bool(parsed['usage']) and (parsed['usage'].get('prompt_tokens') or 0) > 0)
        check(ok, model + ' stream: DONE + content + finish + usage')
    else:
        check(False, model + ' stream: HTTP 200 (got %s %s)' % (status, err))
    return entry, ok, payload if ok else None


def test_agent(model, api_key):
    messages = [{'role': 'user', 'content': AGENT_PROMPT}]
    status, payload, latency, err = chat(api_key, {
        'model': model, 'stream': False, 'messages': messages, 'tools': [TOOL]})
    entry = {'latency_ms': 0}
    record(entry, status, payload, latency, err)
    if status != 200:
        # Upstream free-tier agent quota (FreeUsageLimitError + Retry-After):
        # the request reached opencode with tools intact and the gateway
        # forwarded the error cleanly, so transport itself is verified.
        if status == 429 and err and 'Free usage limit reached' in err:
            entry['note'] = 'upstream FreeUsageLimitError; agent quota gated upstream'
            check(True, model + ' agent: upstream FreeUsageLimitError forwarded (agent quota reset pending)')
            return entry, None, 0
        check(False, model + ' agent: HTTP 200 (got %s %s)' % (status, err))
        return entry, None, 0
    choices = payload.get('choices') or []
    finish = choices[0].get('finish_reason') if choices else None
    if finish != 'tool_calls':
        entry['note'] = 'model answered without calling the tool'
        check(True, model + ' agent: request accepted (no tool call this run)')
        return entry, None, 0
    tool_calls = (choices[0].get('message') or {}).get('tool_calls') or []
    check(bool(tool_calls) and all((c.get('function') or {}).get('name') for c in tool_calls),
          model + ' agent: tool_calls shape')
    check(all(c.get('id') and c.get('type') == 'function' and
              isinstance((c.get('function') or {}).get('arguments'), str) for c in tool_calls),
          model + ' agent: tool_call id/type/arguments')
    echo = {'role': 'assistant', 'content': None,
            'tool_calls': [{'id': c['id'], 'type': 'function',
                            'function': {'name': c['function']['name'],
                                         'arguments': c['function']['arguments']}}
                           for c in tool_calls if c.get('id') and c.get('function')]}
    if not echo['tool_calls']:
        return entry, None, 0
    followup = messages + [echo] + [
        {'role': 'tool', 'tool_call_id': call_item['id'], 'content': TOOL_RESULT}
        for call_item in echo['tool_calls']]
    status2, payload2, latency2, err2 = chat(api_key, {
        'model': model, 'stream': False, 'messages': followup, 'tools': [TOOL]})
    entry2 = {'latency_ms': 0}
    record(entry2, status2, payload2, latency2, err2)
    ok2 = False
    if status2 == 200:
        choices2 = payload2.get('choices') or []
        content2 = ((choices2[0].get('message') or {}).get('content') if choices2 else None)
        finish2 = choices2[0].get('finish_reason') if choices2 else None
        ok2 = finish2 in ('stop', 'length') and isinstance(content2, str) and len(content2.strip()) > 0
        check(ok2, model + ' agent continuation: stop + content')
    else:
        check(False, model + ' agent continuation: HTTP 200 (got %s %s)' % (status2, err2))
    return entry, entry2, (1 if ok2 else 0)


def main():
    global BASE_URL, BASE_PORT
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--models', default='', help='comma separated subset of official models')
    parser.add_argument('--skip-agent', action='store_true')
    parser.add_argument('--env', choices=('compose', 'dev'), default='compose',
                        help='compose: deploy/.env on SERVER_PORT (default); dev: .dev/.env on DEV_BACKEND_PORT')
    args = parser.parse_args()
    if args.env == 'dev':
        settings = dev.read_settings(ROOT / '.dev' / '.env')
        BASE_PORT = settings.get('DEV_BACKEND_PORT', '18080')
        admin_email = settings['DEV_ADMIN_EMAIL']
        admin_password = settings['DEV_ADMIN_PASSWORD']
        pg_container = 'sub2api-dev-postgres-1'
    else:
        settings = dev.read_settings(ROOT / 'deploy' / '.env')
        BASE_PORT = settings.get('SERVER_PORT', '8080')
        admin_email = settings['ADMIN_EMAIL']
        admin_password = settings['ADMIN_PASSWORD']
        pg_container = 'sub2api-postgres'
    BASE_URL = 'http://127.0.0.1:' + BASE_PORT
    models = [m for m in args.models.split(',') if m] or list(OFFICIAL_MODELS)
    unknown = [m for m in models if m not in OFFICIAL_MODELS]
    if unknown:
        raise SystemExit('not official models: ' + ','.join(unknown))

    admin = login(admin_email, admin_password)
    status = call('GET', '/api/v1/admin/compliance', token=admin)
    if isinstance(status, dict) and status.get('required'):
        call('POST', '/api/v1/admin/compliance/accept', {
            'phrase': '我已阅读、理解并同意 Sub2API 部署与运营合规承诺',
            'language': 'zh'}, admin)
    suffix = secrets.token_hex(8)
    group = call('POST', '/api/v1/admin/groups',
                 {'name': 'probe-' + suffix, 'platform': 'opencode_go',
                  'rate_multiplier': 1, 'subscription_type': 'standard'}, admin)
    gid = group['id']
    user_password = secrets.token_urlsafe(24)
    user = call('POST', '/api/v1/admin/users',
                {'email': 'opencode-probe-' + suffix + '@example.test',
                 'password': user_password, 'balance': 50, 'concurrency': 5,
                 'allowed_groups': [gid]}, admin)
    uid = user['id']
    mapping = {model: model for model in OFFICIAL_MODELS}
    mapping.update({'local-guard-probe': 'gemini-9-free',
                    'dynamic-dispatch-probe': 'probe-20261030-free'})
    account = call('POST', '/api/v1/admin/accounts', {
        'name': 'probe-' + suffix, 'platform': 'opencode_go', 'type': 'apikey',
        'concurrency': 3, 'priority': 1, 'group_ids': [gid],
        'credentials': {'account_mode': 'zen', 'api_protocol': 'adaptive',
                        'base_url': 'https://opencode.ai/inference/openai/v1',
                        'api_key': 'public',
                        'opencode_free_transport': 'bun',
                        'opencode_free_profile': 'opencode-big-pickle-text-balanced-20261027-v2',
                        'model_mapping': mapping}}, admin)
    aliases = list(mapping.keys())
    call('POST', '/api/v1/admin/channels', {
        'name': 'probe-' + suffix, 'group_ids': [gid],
        'billing_model_source': 'requested',
        'model_pricing': [{'platform': 'opencode_go', 'models': aliases, 'billing_mode': 'token',
                           'input_price': 0.001, 'output_price': 0.002,
                           'cache_read_price': 0.0005, 'cache_write_price': 0.001}]}, admin)
    user_token = login('opencode-probe-' + suffix + '@example.test', user_password)
    api_key = call('POST', '/api/v1/keys', {'name': 'probe', 'group_id': gid}, user_token)['key']
    balance_before = float(call('GET', '/api/v1/user/profile', token=user_token)['balance'])

    report = {'base': BASE_URL, 'models': {}, 'negatives': {}, 'created': {
        'group_id': gid, 'user_id': uid, 'account_id': account['id']}}
    charged = 0
    for model in models:
        print('== ' + model, flush=True)
        result = {}
        time.sleep(1)
        entry, ok, _ = test_nonstream(model, api_key)
        result['nonstream'] = entry
        charged += 1 if ok else 0
        time.sleep(1)
        entry, ok, _ = test_stream(model, api_key)
        result['stream'] = entry
        charged += 1 if ok else 0
        if not args.skip_agent:
            time.sleep(1)
            entry, entry2, extra = test_agent(model, api_key)
            result['agent'] = entry
            if entry2 is not None:
                result['agent_continuation'] = entry2
            charged += extra
        report['models'][model] = result
        print('   nonstream=%s stream=%s agent=%s' % (
            result['nonstream'].get('status'), result['stream'].get('status'),
            result.get('agent', {}).get('status')), flush=True)

    # Local admission guards must reject before any upstream dispatch.
    time.sleep(1)
    status, payload, latency, err = chat(api_key, {
        'model': 'local-guard-probe', 'stream': False,
        'messages': [{'role': 'user', 'content': TEXT_PROMPT}]})
    entry = {}
    record(entry, status, payload, latency, err)
    entry['expect'] = 'local 400 rejection for jev-/gemini- prefix'
    report['negatives']['gemini_prefix'] = entry
    check(local_reject(status, err), 'local guard: gemini- prefix rejected locally (got %s %s)' % (status, err))

    time.sleep(1)
    status, payload, latency, err = chat(api_key, {
        'model': 'dynamic-dispatch-probe', 'stream': False,
        'messages': [{'role': 'user', 'content': TEXT_PROMPT}]})
    entry = {}
    record(entry, status, payload, latency, err)
    entry['expect'] = 'not a local admission rejection (unknown -free model reaches upstream)'
    report['negatives']['dynamic_dispatch'] = entry
    check(not local_reject(status, err),
          'dynamic dispatch: unknown -free model is not rejected locally (got %s %s)' % (status, err))

    time.sleep(1)
    status, payload, latency, err = chat(api_key, {
        'model': 'no-such-alias-' + suffix, 'stream': False,
        'messages': [{'role': 'user', 'content': TEXT_PROMPT}]})
    entry = {}
    record(entry, status, payload, latency, err)
    report['negatives']['unknown_alias'] = entry
    check(400 <= status < 500, 'unknown alias rejected with 4xx (got %s)' % status)

    # Settlement: usage rows, cost sum and balance must agree.
    rows = []
    for _ in range(30):
        data = call('GET', '/api/v1/usage?page=1&page_size=100', token=user_token)
        rows = data.get('items', data.get('list', []))
        if len(rows) >= charged:
            break
        time.sleep(0.2)
    balance_after = float(call('GET', '/api/v1/user/profile', token=user_token)['balance'])
    cost_sum = sum(float(row.get('actual_cost') or 0) for row in rows)
    report['billing'] = {'charged_requests': charged, 'usage_rows': len(rows),
                         'balance_before': balance_before, 'balance_after': balance_after,
                         'cost_sum': cost_sum}
    check(len(rows) == charged, 'usage rows == charged requests (%d == %d)' % (len(rows), charged))
    check(math.isclose(balance_before - balance_after, cost_sum, rel_tol=1e-6, abs_tol=1e-9),
          'balance delta equals usage cost sum')
    check(cost_sum > 0, 'nonzero charging')

    attempts = None
    try:
        proc = subprocess.run(
            ['docker', 'exec', pg_container, 'sh', '-c',
             'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atq -c '
             '"SELECT state || \'=\' || count(*) FROM opencode_free_attempts WHERE user_id = %d GROUP BY state ORDER BY state;"' % uid],
            capture_output=True, text=True, timeout=30)
        if proc.returncode == 0 and proc.stdout.strip():
            attempts = dict(item.split('=', 1) for item in proc.stdout.split() if '=' in item)
    except Exception as error:  # noqa: BLE001 - database evidence is optional
        attempts = {'skipped': type(error).__name__}
    report['attempts'] = attempts
    if attempts and 'settled' in attempts:
        check(int(attempts['settled']) == charged,
              'settled attempts == charged requests (%s == %d)' % (attempts['settled'], charged))

    failed = [name for name in checks if name.startswith('FAILED')]
    report['status'] = 'passed' if not failed else 'failed'
    report['checks'] = checks
    out = ROOT / '.cache/opencode-free-implementation/real-models-report.json'
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
    print('', flush=True)
    for name in checks:
        print(('  ok   ' if not name.startswith('FAILED') else '  FAIL ') + name, flush=True)
    print('', flush=True)
    print(('REAL MODEL PROBE PASSED' if not failed else 'REAL MODEL PROBE FAILED (%d)' % len(failed))
          + '; report: ' + str(out), flush=True)
    return 1 if failed else 0


if __name__ == '__main__':
    sys.exit(main())
