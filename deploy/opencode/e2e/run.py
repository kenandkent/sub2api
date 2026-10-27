#!/usr/bin/env python3
"""Offline end-to-end test against the isolated native dev backend and mock worker."""
import argparse
import json
import http.client
import math
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / 'deploy'))
import dev

BASE = 'http://127.0.0.1:18080'

def call(method, path, payload=None, token=None, raw=False):
    data = json.dumps(payload).encode() if payload is not None else None
    headers = {'Content-Type':'application/json'}
    if token: headers['Authorization'] = 'Bearer ' + token
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            body = response.read()
            if raw: return body.decode()
            obj = json.loads(body)
            if obj.get('code', 0) != 0: raise AssertionError('API rejected ' + path)
            return obj['data']
    except urllib.error.HTTPError as e:
        raise AssertionError('HTTP %s for %s' % (e.code, path)) from None


def login(email, password):
    return call('POST','/api/v1/auth/login',{'email':email,'password':password})['access_token']


def check(condition, name):
    if not condition: raise AssertionError(name)
    checks.append(name)


def usage(user_token):
    data = call('GET','/api/v1/usage?page=1&page_size=50', token=user_token)
    return data.get('items', data.get('list', []))


def profile(user_token):
    return call('GET','/api/v1/user/profile', token=user_token)


def main():
    global checks
    checks = []
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--compose', action='store_true', help='use the local full Compose deployment and its test-only worker overlay')
    args = parser.parse_args()
    global BASE
    if args.compose:
        settings = dev.read_settings(ROOT / 'deploy/.env')
        BASE = 'http://127.0.0.1:' + settings.get('SERVER_PORT', '8080')
        compose = ['docker','compose','-p','deploy','--env-file','deploy/.env','-f','deploy/docker-compose.local.yml','-f','deploy/docker-compose.opencode-free.yml','-f','deploy/opencode/e2e/docker-compose.mock.yml']
        probe = subprocess.run(compose + ['exec','-T','opencode-free-worker','bun','-e',
            "fetch('http://127.0.0.1:19090/health/e2e').then(r=>r.text()).then(s=>{if(s!=='test-only mock')process.exit(1)}).catch(()=>process.exit(1))"], cwd=ROOT, capture_output=True, text=True)
        check(probe.returncode == 0, 'test-only Compose mock is running')
        email_admin, password_admin = settings['ADMIN_EMAIL'], settings['ADMIN_PASSWORD']
    else:
        with urllib.request.urlopen('http://127.0.0.1:19090/health/e2e', timeout=2) as response:
            check(response.read() == b'test-only mock', 'test-only mock is running')
        settings = dev.read_settings(ROOT / '.dev/.env')
        BASE = 'http://127.0.0.1:' + settings['DEV_BACKEND_PORT']
        compose = ['docker','compose','-p','sub2api-dev','--project-directory','deploy','--env-file','.dev/.env','-f','deploy/docker-compose.dependencies.yml']
        email_admin, password_admin = settings['DEV_ADMIN_EMAIL'], settings['DEV_ADMIN_PASSWORD']
    admin = login(email_admin, password_admin)
    suffix = secrets.token_hex(8)
    email, password = 'opencode-e2e-' + suffix + '@example.test', secrets.token_urlsafe(24)
    group = call('POST','/api/v1/admin/groups',{'name':'opencode-e2e-' + suffix,'platform':'opencode_go','rate_multiplier':1,'subscription_type':'standard'}, admin)
    gid = group['id']
    user = call('POST','/api/v1/admin/users',{'email':email,'password':password,'balance':10,'concurrency':3,'allowed_groups':[gid]}, admin)
    uid = user['id']
    account = call('POST','/api/v1/admin/accounts',{'name':'opencode-e2e-' + suffix,'platform':'opencode_go','type':'apikey','concurrency':3,'priority':1,'group_ids':[gid], 'credentials':{'account_mode':'zen','api_protocol':'adaptive','base_url':'https://opencode.ai/inference/openai/v1','api_key':'public','opencode_free_transport':'bun','opencode_free_profile':'opencode-big-pickle-text-balanced-20261027-v2','model_mapping':{'e2e-alias':'big-pickle'}}}, admin)
    channel = call('POST','/api/v1/admin/channels',{'name':'opencode-e2e-' + suffix,'group_ids':[gid], 'billing_model_source':'requested','model_pricing':[{'platform':'opencode_go','models':['e2e-alias'],'billing_mode':'token','input_price':0.001,'output_price':0.002,'cache_read_price':0.0005,'cache_write_price':0.001}]}, admin)
    user_token = login(email,password)
    api_key = call('POST','/api/v1/keys',{'name':'opencode-e2e','group_id':gid},user_token)
    key = api_key['key']
    before = float(profile(user_token)['balance'])
    def chat(mode, stream):
        result = call('POST','/v1/chat/completions',{'model':'e2e-alias','stream':stream,'messages':[{'role':'user','content':mode}]},key,raw=True)
        return result if stream else json.loads(result)
    result = chat('normal',False)
    check(result['choices'][0]['message']['content'] == 'fixture response' and result['choices'][0]['finish_reason'] == 'stop','nonstream content and finish')
    stream = chat('normal',True)
    check('fixture response' in stream and '"finish_reason":"stop"' in stream and '[DONE]' in stream and '"prompt_tokens":10' in stream,'stream content finish usage and DONE')
    # The free ledger is settled synchronously; allow bounded cache propagation.
    for _ in range(30):
        rows = usage(user_token)
        if len(rows) >= 2: break
        time.sleep(.2)
    check(len(rows) == 2,'two usage records')
    for row in rows:
        check(row['output_tokens'] == 2 and row['cache_read_tokens'] == 3 and row['input_tokens'] + row['cache_read_tokens'] == 10,'usage token buckets')
        check(math.isclose(float(row['input_cost']), 0.007, abs_tol=1e-8) and math.isclose(float(row['output_cost']), 0.004, abs_tol=1e-8) and math.isclose(float(row['cache_read_cost']), 0.0015, abs_tol=1e-8) and math.isclose(float(row['actual_cost']), 0.0125, abs_tol=1e-8), 'nonzero price and cache buckets charged')
    after = float(profile(user_token)['balance'])
    check(math.isclose(before-after, sum(float(row['actual_cost']) for row in rows), rel_tol=1e-7, abs_tol=1e-9),'balance matches usage logs')
    incomplete = chat('missing-usage',True)
    check('[DONE]' not in incomplete,'missing usage suppresses DONE')
    time.sleep(.5)
    check(len(usage(user_token)) == 2 and math.isclose(float(profile(user_token)['balance']),after,abs_tol=1e-9),'missing usage does not charge')
    conn = http.client.HTTPConnection('127.0.0.1', int(BASE.rsplit(':',1)[1]), timeout=10)
    conn.request('POST', '/v1/chat/completions', json.dumps({'model':'e2e-alias','stream':True,'messages':[{'role':'user','content':'cancel'}]}), {'Content-Type':'application/json','Authorization':'Bearer ' + key})
    response = conn.getresponse()
    check(response.status == 200 and b'fixture response' in response.readline(), 'cancel receives initial content')
    response.close()
    conn.close()
    time.sleep(1)
    check(len(usage(user_token)) == 2 and math.isclose(float(profile(user_token)['balance']),after,abs_tol=1e-9),'cancel does not charge')
    sql = ("SELECT state || ':' || count(*) FROM opencode_free_attempts WHERE user_id = " + str(int(uid)) + " GROUP BY state ORDER BY state; SELECT 'metadata:' || count(*) FROM opencode_free_attempts WHERE user_id = " + str(int(uid)) + " AND state='settled' AND billing_source='free' AND raw_usage->'opencode'->'tokens'->>'input'='7';")
    proc = subprocess.run(compose + ['exec','-T','postgres','sh','-c', 'psql -U \"$POSTGRES_USER\" -d \"$POSTGRES_DB\" -Atq'], cwd=ROOT, input=sql, text=True, capture_output=True)
    check(proc.returncode == 0 and 'settled:2' in proc.stdout and 'unknown:2' in proc.stdout and 'metadata:2' in proc.stdout, 'settled and incomplete attempts and raw usage recorded')
    check(len({row['request_id'] for row in rows}) == 2, 'settlements have distinct request IDs')
    private_path = ROOT / '.cache/opencode-free-implementation/e2e-private.json'
    private_path.parent.mkdir(parents=True,exist_ok=True)
    temp_path = private_path.with_name('e2e-private-' + secrets.token_hex(8) + '.tmp')
    fd = os.open(temp_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump({'email':email,'password':password,'api_key':key,'user_token':user_token,'user_id':uid,'api_key_id':api_key['id'],'group_id':gid,'account_id':account['id'],'channel_id':channel['id'],'base_url':BASE,'mode':'compose' if args.compose else 'native'}, stream)
        os.replace(temp_path, private_path)
    finally:
        if temp_path.exists(): temp_path.unlink()
    report = {'status':'passed','checks':checks,'test_user_id':uid,'test_group_id':gid,'notes':['deduplication requires separate repository verification']}
    path = ROOT / '.cache/opencode-free-implementation/e2e-report.json'
    path.parent.mkdir(parents=True,exist_ok=True)
    path.write_text(json.dumps(report,indent=2)+'\n')
    print('Offline E2E passed; sanitized report: ' + str(path))

if __name__ == '__main__':
    try: main()
    except Exception as exc:
        # Never print or persist response bodies, tokens or environment values.
        report = {'status':'failed','checks':globals().get('checks',[]),'error_type':type(exc).__name__}
        if isinstance(exc, AssertionError): report['failed_check'] = str(exc)
        path = ROOT / '.cache/opencode-free-implementation/e2e-report.json'
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(report, indent=2)+'\n')
        print('Offline E2E failed: ' + type(exc).__name__ + (' - ' + str(exc) if isinstance(exc, AssertionError) else ''), file=sys.stderr)
        sys.exit(1)
