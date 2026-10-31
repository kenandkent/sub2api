import { test, expect } from 'bun:test';

test('worker exposes an authenticated transport factory', async () => {
  let factory: unknown;
  try { factory = (await import('./worker')).createWorker; } catch {}
  expect(typeof factory).toBe('function');
});

const TOKEN = 'a'.repeat(32);
const PROFILES = new URL('../profiles', import.meta.url).pathname;
const HASH = 'da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9';
const ID = 'opencode-big-pickle-text-balanced-20261027-v2';
const envelope = () => ({version: 1, request_id: 'req-1', profile_id: ID, profile_sha256: HASH, target: 'console', account_id: 1, session_id: 'ses_0123456789ab0123456789abcd', deadline_unix_ms: Date.now()+5000, body: {model:'big-pickle', messages:[{role:'user',content:'hello'}]}});
const request = (body: unknown, token = TOKEN) => new Request('http://localhost/v1/transport/chat', {method:'POST', headers:{authorization:`Bearer ${token}`, 'content-type':'application/json'}, body:JSON.stringify(body)});
const READ_PLACEHOLDER = {type:'function',function:{name:'read',description:'Do not call this tool. It exists only for API compatibility and must never be invoked.',parameters:{type:'object',properties:{filePath:{type:'string'}},required:['filePath']}}};

test('injects pinned profile once and preserves caller messages', async () => {
  const {createWorker} = await import('./worker');
  let payload: any, options: any, url: any;
  const worker = createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async (u: any,o: any) => {url=u; options=o; payload=JSON.parse(o.body); return new Response('data: [DONE]\n\n',{headers:{'content-type':'text/event-stream','x-sub2api-transport-result':'evil'}});}});
  const response = await worker(request(envelope()));
  expect(response.status).toBe(200);
  expect(response.headers.get('x-sub2api-transport-result')).toBe('upstream');
  expect(response.headers.get('x-sub2api-dispatched')).toBe('true');
  expect(await response.text()).toBe('data: [DONE]\n\n');
  expect(url).toBe('https://opencode.ai/inference/openai/v1/chat/completions');
  expect(payload.messages).toEqual([{role:'system',content:expect.stringContaining('You are an AI agent running in OpenCode')},{role:'user',content:'hello'}]);
  expect(payload.tools).toEqual([{type:'function',function:{name:'read'}}]);
  expect(payload.temperature).toBe(0);
  expect(payload.tool_choice).toBe('none');
  expect(payload.stream_options).toEqual({include_usage:true});
  expect(options.keepalive).toBe(false);
  expect(options.redirect).toBe('error');
});

test('rejects unauthorized and unsupported requests before dispatch', async () => {
  const {createWorker} = await import('./worker');
  let calls = 0;
  const worker = createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async () => {calls++; return new Response();}});
  expect((await worker(request(envelope(),'wrong'))).status).toBe(401);
  for (const body of [
    {model:'big-pickle',messages:[{role:'user',content:'hello'}],temperature:0.1},
    {model:'big-pickle',messages:[{role:'tool',content:'hello'}]},
    {model:'big-pickle',messages:[{role:'user',content:[{type:'text',text:'hello'}]}]},
    {model:'big-pickle',messages:[{role:'user',content:'hello'}],tools:[{type:'function'}]},
    {model:'big-pickle',messages:[{role:'user',content:'hello'}],top_p:0.5},
    {model:'big-pickle',messages:[{role:'user',content:'hello'}],n:2},
  ]) {
    const res = await worker(request({...envelope(),body}));
    expect(res.status).toBe(400);
    expect(res.headers.get('x-sub2api-dispatched')).toBe('false');
  }
  expect(calls).toBe(0);
});

test('rejects corrupted profile at startup and identity mismatch before dispatch', async () => {
  const {createWorker} = await import('./worker');
  const fs = await import('node:fs');
  const path = await import('node:path');
  const os = await import('node:os');
  expect(() => createWorker({token:TOKEN,profilesDir:'/__missing_profiles__'})).toThrow();
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'opencode-free-profiles-'));
  try {
    fs.cpSync(PROFILES, tmp, {recursive: true});
    const target = path.join(tmp, 'glm-5.3-flash-free-text-balanced.v1.json');
    fs.writeFileSync(target, fs.readFileSync(target).toString() + ' ');
    expect(() => createWorker({token:TOKEN,profilesDir:tmp})).toThrow();
  } finally {fs.rmSync(tmp, {recursive: true, force: true});}
  let calls=0;
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>{calls++; return new Response();}});
  expect((await worker(request({...envelope(),profile_sha256:'wrong'}))).status).toBe(400);
  expect(calls).toBe(0);
});

test('rejects unsupported proxy protocols without dispatch', async () => {
  const {createWorker} = await import('./worker'); let calls=0;
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>{calls++;return new Response();}});
  for (const url of ['socks5://127.0.0.1:1080','https://127.0.0.1:8080']) {
    const res=await worker(request({...envelope(),proxy:{id:1,generation:'1',url}}));
    expect(res.status).toBe(400); expect(res.headers.get('x-sub2api-dispatched')).toBe('false');
  }
  expect(calls).toBe(0);
});

test('aborts upstream when the real downstream socket disconnects', async () => {
  const {createWorker} = await import('./worker');
  let aborted = false;
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async (_:any, opts:any) => {
    opts.signal.addEventListener('abort',()=>{aborted=true;});
    return new Response(new ReadableStream({start(c){c.enqueue(new TextEncoder().encode('data: first\n\n'));}}),{headers:{'content-type':'text/event-stream'}});
  }});
  const server=Bun.serve({port:0,hostname:'127.0.0.1',fetch:worker});
  const client=await import('node:net');
  try {
    const socket=client.connect(server.port,'127.0.0.1');
    const body=JSON.stringify(envelope());
    socket.write(`POST /v1/transport/chat HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer ${TOKEN}\r\nContent-Length: ${Buffer.byteLength(body)}\r\n\r\n${body}`);
    await new Promise<void>((resolve,reject)=>{socket.once('data',()=>resolve());socket.once('error',reject);});
    socket.destroy();
    for(let i=0;i<100 && !aborted;i++) await Bun.sleep(10);
    expect(aborted).toBe(true);
  } finally {server.stop(true);}
});

test('deadline aborts an in-progress upstream response', async () => {
  const {createWorker} = await import('./worker'); let aborted=false;
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async (_:any, opts:any) => {opts.signal.addEventListener('abort',()=>{aborted=true;});return new Response(new ReadableStream({start(c){c.enqueue(new TextEncoder().encode('data: x\n\n'));}}),{headers:{'content-type':'text/event-stream'}});}});
  const res=await worker(request({...envelope(),deadline_unix_ms:Date.now()+50}));
  const reader=res.body!.getReader(); await reader.read(); await Bun.sleep(100);
  expect(aborted).toBe(true); await reader.cancel().catch(()=>{});
});

test('worker health probes never invoke upstream', async () => {
  const {createWorker} = await import('./worker'); let calls=0;
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>{calls++;return new Response();}});
  for(const path of ['/health/live','/health/ready']) expect((await worker(new Request(`http://localhost${path}`))).status).toBe(200);
  expect(calls).toBe(0);
});

test('concurrent HTTP proxy requests reach their own CONNECT sockets', async () => {
  const {createWorker} = await import('./worker'); const net=await import('node:net');
  const seen:string[]=[];
  const proxies=await Promise.all(['one','two'].map(label=>new Promise<any>(resolve=>{const s=net.createServer(socket=>{socket.once('data',data=>{seen.push(`${label}:${data.toString().split('\r\n')[0]}`);socket.end('HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n');});});s.listen(0,'127.0.0.1',()=>resolve(s));})));
  try {
    const worker=createWorker({token:TOKEN,profilesDir:PROFILES});
    const results=await Promise.all(proxies.map((s,i)=>worker(request({...envelope(),request_id:`req-${i}`,proxy:{id:i+1,generation:'1',url:`http://127.0.0.1:${s.address().port}`}}))));
    expect(results.map(r=>r.status)).toEqual([502,502]);
    expect(seen.sort()).toEqual(['one:CONNECT opencode.ai:443 HTTP/1.1','two:CONNECT opencode.ai:443 HTTP/1.1']);
  } finally {for(const s of proxies) s.close();}
});

test('oversized SSE frame terminates the stream without a synthetic DONE', async () => {
  const {createWorker} = await import('./worker');
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response(new ReadableStream({start(c){c.enqueue(new Uint8Array(256*1024+1).fill(97));c.close();}}),{headers:{'content-type':'text/event-stream'}})});
  const res=await worker(request(envelope()));
  await expect(res.text()).rejects.toThrow();
});

test('slow socket reader eventually stops upstream pulls', async () => {
  const {createWorker} = await import('./worker'); const net=await import('node:net'); let pulls=0;
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response(new ReadableStream({pull(c){pulls++;c.enqueue(new TextEncoder().encode(`data: ${'x'.repeat(16000)}\n\n`));}}, {highWaterMark:0}),{headers:{'content-type':'text/event-stream'}})});
  const server=Bun.serve({port:0,hostname:'127.0.0.1',fetch:worker});
  const socket=net.connect(server.port,'127.0.0.1');
  try {
    const body=JSON.stringify(envelope()); socket.write(`POST /v1/transport/chat HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer ${TOKEN}\r\nContent-Length: ${Buffer.byteLength(body)}\r\n\r\n${body}`);
    await new Promise<void>((resolve,reject)=>{socket.once('data',()=>{socket.pause();resolve();});socket.once('error',reject);});
    await Bun.sleep(250); const first=pulls; await Bun.sleep(250);
    expect(first).toBeGreaterThan(0); expect(pulls).toBeLessThan(512); expect(pulls-first).toBeLessThan(20);
  } finally {socket.destroy(); server.stop(true);}
});

test('HTTP proxy credentials are sent only to the selected proxy', async () => {
  const {createWorker}=await import('./worker'); const net=await import('node:net'); let first='';
  const server=net.createServer(socket=>socket.once('data',data=>{first=data.toString();socket.end('HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n');}));
  await new Promise<void>(resolve=>server.listen(0,'127.0.0.1',resolve));
  try {
    const worker=createWorker({token:TOKEN,profilesDir:PROFILES});
    await worker(request({...envelope(),proxy:{id:1,generation:'1',url:`http://demo:demo@127.0.0.1:${(server.address() as any).port}`}}));
    expect(first.toLowerCase()).toContain('proxy-authorization: basic zgvtbzpkzw1v');
  } finally {server.close();}
});

test('HTTP proxy carries a verified TLS tunnel and preserves its response', async () => {
  const net=await import('node:net'); const fs=await import('node:fs'); const path=await import('node:path');
  const dir=fs.mkdtempSync(path.join(import.meta.dir,'tls-test-'));
  let tlsServer:any, proxy:any;
  try {
    const cert=path.join(dir,'cert.pem'), key=path.join(dir,'key.pem');
    const make=Bun.spawnSync(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-subj','/CN=opencode.ai','-addext','subjectAltName=DNS:opencode.ai','-keyout',key,'-out',cert],{stdout:'ignore',stderr:'ignore'});
    expect(make.exitCode).toBe(0);
    tlsServer=Bun.serve({port:0,hostname:'127.0.0.1',tls:{key:fs.readFileSync(key),cert:fs.readFileSync(cert)},fetch:()=>new Response('data: verified\n\ndata: [DONE]\n\n',{headers:{'content-type':'text/event-stream'}})});
    proxy=net.createServer(socket=>socket.once('data',data=>{if(!data.toString().startsWith('CONNECT opencode.ai:443 ')) {socket.destroy(); return;} const upstream=net.connect(tlsServer.port,'127.0.0.1',()=>{socket.write('HTTP/1.1 200 Connection Established\r\n\r\n');socket.pipe(upstream);upstream.pipe(socket);});}));
    await new Promise<void>(resolve=>proxy.listen(0,'127.0.0.1',resolve));
    const data={...envelope(),proxy:{id:1,generation:'1',url:`http://127.0.0.1:${(proxy.address() as any).port}`}};
    const script=`import {createWorker} from ${JSON.stringify(path.join(import.meta.dir,'worker.ts'))}; const worker=createWorker({token:${JSON.stringify(TOKEN)},profilesDir:${JSON.stringify(PROFILES)}}); const response=await worker(new Request('http://localhost/v1/transport/chat',{method:'POST',headers:{authorization:${JSON.stringify('Bearer '+TOKEN)}},body:${JSON.stringify(JSON.stringify(data))}})); console.log(JSON.stringify({status:response.status,body:await response.text()}));`;
    const child=Bun.spawn([process.execPath,'-e',script],{env:{...process.env,SSL_CERT_FILE:cert,BUN_RUNTIME_TRANSPILER_CACHE_PATH:'0'},stdout:'pipe',stderr:'pipe'});
    const output=await new Response(child.stdout).text(); const err=await new Response(child.stderr).text();
    expect(await child.exited).toBe(0);
    expect(JSON.parse(output)).toEqual({status:200,body:'data: verified\n\ndata: [DONE]\n\n'});
    const untrusted=Bun.spawn([process.execPath,'-e',script],{env:{...process.env,SSL_CERT_FILE:'',BUN_RUNTIME_TRANSPILER_CACHE_PATH:'0'},stdout:'pipe',stderr:'ignore'});
    const rejected=JSON.parse(await new Response(untrusted.stdout).text());
    expect(await untrusted.exited).toBe(0); expect(rejected.status).toBe(502);
  } finally {proxy?.close();tlsServer?.stop(true);fs.rmSync(dir,{recursive:true,force:true});}
});

test('response frame bound applies even when upstream mislabels content type', async () => {
  const {createWorker}=await import('./worker');
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response(new Uint8Array(256*1024+1).fill(97),{headers:{'content-type':'application/json'}})});
  await expect((await worker(request(envelope()))).text()).rejects.toThrow();
});

test('deadline interrupts a stalled response read and cancels its source', async () => {
  const {createWorker}=await import('./worker'); let cancelled=false;
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response(new ReadableStream({cancel(){cancelled=true;}}))});
  const response=await worker(request({...envelope(),deadline_unix_ms:Date.now()+40}));
  const read=response.body!.getReader().read().then(()=> 'completed',()=> 'rejected');
  expect(await Promise.race([read,Bun.sleep(150).then(()=> 'stalled')])).toBe('rejected');
  expect(cancelled).toBe(true);
});

test('proxy CONNECT rejection is a dispatched worker error without proxy body', async () => {
  const {createWorker}=await import('./worker'); const net=await import('node:net');
  for (const status of [403,429]) {
    const proxy=net.createServer(socket=>socket.once('data',()=>socket.end(`HTTP/1.1 ${status} Rejected\r\nContent-Length: 14\r\n\r\nsecret-from-proxy`)));
    await new Promise<void>(resolve=>proxy.listen(0,'127.0.0.1',resolve));
    try {
      const worker=createWorker({token:TOKEN,profilesDir:PROFILES});
      const res=await worker(request({...envelope(),proxy:{id:1,generation:'1',url:`http://127.0.0.1:${(proxy.address() as any).port}`}}));
      expect(res.status).toBe(502);
      expect(res.headers.get('x-sub2api-transport-result')).toBe('worker-error');
      expect(res.headers.get('x-sub2api-dispatched')).toBe('true');
      const data=await res.json(); expect(data.error.code).toBe('proxy_or_upstream_http_error');
      expect(JSON.stringify(data)).not.toContain('secret-from-proxy');
    } finally {proxy.close();}
  }
});

const captureWorkerLogs = () => {
  const lines: any[] = [];
  const original = console.error;
  console.error = (...args: any[]) => {try { lines.push(JSON.parse(String(args[0])));} catch {}};
  return {lines, restore: () => {console.error = original;}};
};

test('proxy upstream quota rejection keeps its real status and is logged', async () => {
  const {createWorker}=await import('./worker');
  const body=JSON.stringify({error:{code:'FreeUsageLimitError',message:'Free usage limit reached'}});
  const logs=captureWorkerLogs();
  try {
    const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response(body,{status:429,headers:{'content-type':'application/json','retry-after':'74005','x-request-id':'req_upstream_1'}})});
    const res=await worker(request({...envelope(),proxy:{id:7,generation:'gen7',url:'http://127.0.0.1:1'}}));
    expect(res.status).toBe(429);
    expect(res.headers.get('x-sub2api-transport-result')).toBe('upstream');
    expect(res.headers.get('x-sub2api-dispatched')).toBe('true');
    expect(res.headers.get('retry-after')).toBe('74005');
    expect(res.headers.get('x-request-id')).toBe('req_upstream_1');
    expect(await res.text()).toBe(body);
  } finally {logs.restore();}
  const entry=logs.lines.find(l=>l?.event==='proxy_upstream_non_200');
  expect(entry).toBeDefined();
  expect(entry.level).toBe('error');
  expect(entry.classified).toBe('upstream');
  expect(entry.upstream_status).toBe(429);
  expect(entry.retry_after).toBe('74005');
  expect(entry.content_type).toBe('application/json');
  expect(entry.request_id).toBe('req-1');
  expect(entry.account_id).toBe(1);
  expect(entry.profile_id).toBe(ID);
  expect(entry.proxy_id).toBe(7);
  expect(entry.proxy_generation).toBe('gen7');
  expect(entry.model).toBe('big-pickle');
  expect(typeof entry.dispatch_ms).toBe('number');
  expect(entry.body_truncated).toBe(false);
});

test('proxy junk without upstream evidence stays an opaque logged 502', async () => {
  const {createWorker}=await import('./worker');
  const logs=captureWorkerLogs();
  try {
    const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response('<html>denied http://user:pass@proxy.example:8080 try Bearer abc.def-123</html>',{status:403,headers:{'content-type':'text/html'}})});
    const res=await worker(request({...envelope(),proxy:{id:2,generation:'gen2',url:'http://127.0.0.1:1'}}));
    expect(res.status).toBe(502);
    expect(res.headers.get('x-sub2api-transport-result')).toBe('worker-error');
    const data=await res.json(); expect(data.error.code).toBe('proxy_or_upstream_http_error');
    expect(JSON.stringify(data)).not.toContain('user:pass');
  } finally {logs.restore();}
  const entry=logs.lines.find(l=>l?.event==='proxy_upstream_non_200');
  expect(entry).toBeDefined();
  expect(entry.classified).toBe('proxy_error');
  expect(entry.upstream_status).toBe(403);
  expect(entry.content_type).toBe('text/html');
  expect(entry.body_snippet).toContain('http://***:***@');
  expect(entry.body_snippet).not.toContain('user:pass');
  expect(entry.body_snippet).toContain('Bearer ***');
  expect(entry.body_snippet).not.toContain('abc.def-123');
});

test('proxy upstream JSON error envelope is forwarded with its status', async () => {
  const {createWorker}=await import('./worker');
  const body=JSON.stringify({error:{message:'bad request parameters'}});
  const logs=captureWorkerLogs();
  try {
    const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response(body,{status:400,headers:{'content-type':'application/json'}})});
    const res=await worker(request({...envelope(),proxy:{id:3,generation:'gen3',url:'http://127.0.0.1:1'}}));
    expect(res.status).toBe(400);
    expect(res.headers.get('x-sub2api-transport-result')).toBe('upstream');
    expect(await res.text()).toBe(body);
  } finally {logs.restore();}
  const entry=logs.lines.find(l=>l?.event==='proxy_upstream_non_200');
  expect(entry.classified).toBe('upstream');
  expect(entry.upstream_status).toBe(400);
});

test('proxy oversized non-200 body keeps the conservative 502', async () => {
  const {createWorker}=await import('./worker');
  const logs=captureWorkerLogs();
  try {
    const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response(new Uint8Array(70*1024).fill(120),{status:418,headers:{'content-type':'text/plain','x-request-id':'req_big'}})});
    const res=await worker(request({...envelope(),proxy:{id:4,generation:'gen4',url:'http://127.0.0.1:1'}}));
    expect(res.status).toBe(502);
    const data=await res.json(); expect(data.error.code).toBe('proxy_or_upstream_http_error');
  } finally {logs.restore();}
  const entry=logs.lines.find(l=>l?.event==='proxy_upstream_non_200');
  expect(entry.classified).toBe('proxy_error');
  expect(entry.body_truncated).toBe(true);
  expect(entry.upstream_status).toBe(418);
  expect(entry.body_bytes).toBeGreaterThan(64*1024);
});

test('dispatch failures are logged before returning upstream_error', async () => {
  const {createWorker}=await import('./worker');
  const logs=captureWorkerLogs();
  try {
    const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>{throw new Error('ECONNRESET after CONNECT');}});
    const res=await worker(request(envelope()));
    expect(res.status).toBe(502);
    const data=await res.json(); expect(data.error.code).toBe('upstream_error');
  } finally {logs.restore();}
  const entry=logs.lines.find(l=>l?.event==='dispatch_failed');
  expect(entry).toBeDefined();
  expect(entry.dispatched).toBe(true);
  expect(entry.request_id).toBe('req-1');
  expect(entry.error_name).toBe('Error');
  expect(entry.error_message).toContain('ECONNRESET');
});

test('slow real socket uploads release all slots after the body timeout', async () => {
  const {createWorker}=await import('./worker'); const net=await import('node:net');
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,bodyReadTimeoutMs:100,fetchImpl:async()=>new Response('ok')});
  const server=Bun.serve({port:0,hostname:'127.0.0.1',fetch:worker});
  const sockets: import('node:net').Socket[]=[];
  try {
    await Promise.all(Array.from({length:64},()=>new Promise<void>((resolve,reject)=>{
      const socket=net.connect(server.port,'127.0.0.1',()=>{socket.write(`POST /v1/transport/chat HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer ${TOKEN}\r\nContent-Length: 1000\r\n\r\n{`);resolve();}); socket.on('error',reject); sockets.push(socket);
    })));
    await Bun.sleep(300);
    const response=await fetch(`http://127.0.0.1:${server.port}/v1/transport/chat`,{method:'POST',headers:{authorization:`Bearer ${TOKEN}`},body:JSON.stringify(envelope())});
    expect(response.status).toBe(200);
    expect(await response.text()).toBe('ok');
  } finally {for(const socket of sockets) socket.destroy();server.stop(true);}
});

test('aborted partial socket uploads release slots before body timeout', async () => {
  const {createWorker}=await import('./worker'); const net=await import('node:net');
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response('ok')});
  const server=Bun.serve({port:0,hostname:'127.0.0.1',fetch:worker}); const sockets: import('node:net').Socket[]=[];
  try {
    await Promise.all(Array.from({length:64},()=>new Promise<void>((resolve,reject)=>{
      const socket=net.connect(server.port,'127.0.0.1',()=>{socket.write(`POST /v1/transport/chat HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer ${TOKEN}\r\nContent-Length: 1000\r\n\r\n{`);resolve();}); socket.on('error',reject);sockets.push(socket);
    })));
    await Bun.sleep(75); for (const socket of sockets) socket.destroy();
    await Bun.sleep(100);
    const response=await fetch(`http://127.0.0.1:${server.port}/v1/transport/chat`,{method:'POST',headers:{authorization:`Bearer ${TOKEN}`},body:JSON.stringify(envelope())});
    expect(response.status).toBe(200); expect(await response.text()).toBe('ok');
  } finally {for(const socket of sockets) socket.destroy();server.stop(true);}
});

test('direct upstream errors preserve their upstream provenance', async () => {
  const {createWorker}=await import('./worker');
  const worker=createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>new Response('unavailable',{status:429})});
  const res=await worker(request(envelope()));
  expect(res.status).toBe(429); expect(res.headers.get('x-sub2api-transport-result')).toBe('upstream');
  expect(await res.text()).toBe('unavailable');
});

const FOO_TEXT_ID = 'opencode-foo-free-text-balanced-20261027-v1';
const FOO_TEXT_SHA = 'dbf2692e129e259fdc39b912a0df94bd65b3c485492ec4d4edd8fff734126886';
const FOO_AGENT_ID = 'opencode-foo-free-agent-balanced-20261027-v1';
const FOO_AGENT_SHA = '3f364c46161820efa4a0bbec645d1f9d83a4b0a04e023f29c10645d1c6b77ed3';

test('derived profiles reproduce the gateway golden hashes and admission rules', async () => {
  const {createRegistry, admissibleModel} = await import('./profiles');
  const registry = createRegistry({profilesDir: PROFILES});
  const text = registry.resolve('foo-free', FOO_TEXT_ID, FOO_TEXT_SHA);
  expect(text?.mode).toBe('text');
  expect(text?.profile.model).toBe('foo-free');
  expect(text?.profile.status).toBe('candidate-unvalidated');
  const agent = registry.resolve('foo-free', FOO_AGENT_ID, FOO_AGENT_SHA);
  expect(agent?.mode).toBe('agent');
  expect(agent?.profile.model).toBe('foo-free');
  // identity must bind id and hash to the same mode
  expect(registry.resolve('foo-free', FOO_TEXT_ID, FOO_AGENT_SHA)).toBeUndefined();
  // pinned models never accept a derived identity
  expect(registry.resolve('big-pickle', FOO_TEXT_ID, FOO_TEXT_SHA)).toBeUndefined();
  expect(registry.resolve('big-pickle', 'opencode-big-pickle-text-balanced-20261027-v1', FOO_TEXT_SHA)).toBeUndefined();
  expect(registry.resolve('mimo-v2.6-flash-free', FOO_TEXT_ID, FOO_TEXT_SHA)).toBeUndefined();
  for (const model of ['foo-free', 'x-free', 'nemotron-9-ultra-free']) expect(admissibleModel(model)).toBe(true);
  for (const model of ['gpt-4o', 'jev-1.13-free', 'gemini-2.5-pro-free', 'FOO-free', 'foo_free', '-free', 'big-pickle-plus']) expect(admissibleModel(model)).toBe(false);
  expect(admissibleModel('a'.repeat(70) + '-free')).toBe(false);
});

test('dispatches a derived text profile for a newly admitted free model', async () => {
  const {createWorker} = await import('./worker');
  let payload: any;
  const worker = createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async (_u: any,o: any) => {payload = JSON.parse(o.body); return new Response('data: [DONE]\n\n',{headers:{'content-type':'text/event-stream'}});}});
  const res = await worker(request({...envelope(), profile_id: FOO_TEXT_ID, profile_sha256: FOO_TEXT_SHA, body: {model:'foo-free', messages:[{role:'user',content:'hello'}], max_tokens: 128}}));
  expect(res.status).toBe(200);
  expect(payload.model).toBe('foo-free');
  expect(payload.messages).toEqual([{role:'system',content:expect.stringContaining('You are an AI agent running in OpenCode')},{role:'user',content:'hello'}]);
  expect(payload.tools).toEqual([{type:'function',function:{name:'read'}}]);
  expect(payload.tool_choice).toBe('none');
  expect(payload.max_tokens).toBe(128);
  expect(payload.temperature).toBe(0);
  expect(payload.stream).toBe(true);
});

test('dispatches the derived agent profile with caller tools and tool history', async () => {
  const {createWorker} = await import('./worker');
  let payload: any;
  const worker = createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async (_u: any,o: any) => {payload = JSON.parse(o.body); return new Response('data: [DONE]\n\n',{headers:{'content-type':'text/event-stream'}});}});
  const body = {
    model:'foo-free',
    messages:[
      {role:'user',content:'look it up'},
      {role:'assistant',content:null,tool_calls:[{id:'call_1',type:'function',function:{name:'lookup',arguments:'{}'}}]},
      {role:'tool',tool_call_id:'call_1',content:'result'},
    ],
    tools:[{type:'function',function:{name:'lookup',description:'d',parameters:{type:'object',properties:{city:{type:'string',enum:['Paris','Tokyo']}},required:['city'],anyOf:[{required:['city']}],additionalProperties:false}}}],
    parallel_tool_calls:false,
  };
  const res = await worker(request({...envelope(), profile_id: FOO_AGENT_ID, profile_sha256: FOO_AGENT_SHA, body}));
  expect(res.status).toBe(200);
  expect(payload.model).toBe('foo-free');
  expect(payload.messages[0].role).toBe('system');
  expect(payload.messages.slice(1)).toEqual(body.messages);
  expect(payload.tools).toEqual([...body.tools, READ_PLACEHOLDER]);
  expect(payload.tool_choice).toBe('auto');
  expect(payload.parallel_tool_calls).toBe(false);
});

test('keeps caller tools untouched when they already declare the read placeholder', async () => {
  const {createWorker} = await import('./worker');
  let payload: any;
  const worker = createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async (_u: any,o: any) => {payload = JSON.parse(o.body); return new Response('data: [DONE]\n\n',{headers:{'content-type':'text/event-stream'}});}});
  const tools = [READ_PLACEHOLDER, {type:'function',function:{name:'lookup',description:'d',parameters:{type:'object',properties:{q:{type:'string'}}}}}];
  const res = await worker(request({...envelope(), profile_id: FOO_AGENT_ID, profile_sha256: FOO_AGENT_SHA, body: {model:'foo-free', messages:[{role:'user',content:'hi'}], tools}}));
  expect(res.status).toBe(200);
  expect(payload.tools).toEqual(tools);
  expect(payload.tools.filter((t: any) => t.function.name === 'read').length).toBe(1);
  expect(payload.tool_choice).toBe('auto');
});

test('agent profile without caller tools falls back to profile tools pinned to none', async () => {
  const {createWorker} = await import('./worker');
  let payload: any;
  const worker = createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async (_u: any,o: any) => {payload = JSON.parse(o.body); return new Response('data: [DONE]\n\n',{headers:{'content-type':'text/event-stream'}});}});
  const res = await worker(request({...envelope(), profile_id: FOO_AGENT_ID, profile_sha256: FOO_AGENT_SHA, body: {model:'foo-free', messages:[{role:'user',content:'hi'}]}}));
  expect(res.status).toBe(200);
  expect(payload.tools).toEqual([{type:'function',function:{name:'read'}}]);
  expect(payload.tool_choice).toBe('none');
});

test('rejects inadmissible models, bad identities and invalid agent bodies before dispatch', async () => {
  const {createWorker} = await import('./worker');
  let calls = 0;
  const worker = createWorker({token:TOKEN,profilesDir:PROFILES,fetchImpl:async()=>{calls++; return new Response();}});
  const cases: {envelope: any, expect: number}[] = [
    // jev-/gemini- models never derive even with a self-consistent identity
    {envelope: {...envelope(), profile_id: FOO_TEXT_ID, profile_sha256: FOO_TEXT_SHA, body: {model:'jev-1.13-free', messages:[{role:'user',content:'hi'}]}}, expect: 400},
    {envelope: {...envelope(), profile_id: FOO_TEXT_ID, profile_sha256: FOO_TEXT_SHA, body: {model:'gemini-2.5-pro-free', messages:[{role:'user',content:'hi'}]}}, expect: 400},
    // pinned model with a derived identity
    {envelope: {...envelope(), profile_id: FOO_TEXT_ID, profile_sha256: FOO_TEXT_SHA, body: {model:'big-pickle', messages:[{role:'user',content:'hi'}]}}, expect: 400},
    // correct derived id with the wrong hash
    {envelope: {...envelope(), profile_id: FOO_TEXT_ID, profile_sha256: FOO_AGENT_SHA, body: {model:'foo-free', messages:[{role:'user',content:'hi'}]}}, expect: 400},
    // agent identity with a body that is only valid as text
    {envelope: {...envelope(), profile_id: FOO_AGENT_ID, profile_sha256: FOO_AGENT_SHA, body: {model:'foo-free', messages:[{role:'tool',tool_call_id:'x',content:'r'}], tools:[{type:'function',function:{name:'x'}}]}}, expect: 400},
    // unmatched tool result in agent history
    {envelope: {...envelope(), profile_id: FOO_AGENT_ID, profile_sha256: FOO_AGENT_SHA, body: {model:'foo-free', messages:[{role:'user',content:'x'},{role:'tool',tool_call_id:'missing',content:'r'}], tools:[{type:'function',function:{name:'x'}}]}}, expect: 400},
    // forced tool_choice not declared
    {envelope: {...envelope(), profile_id: FOO_AGENT_ID, profile_sha256: FOO_AGENT_SHA, body: {model:'foo-free', messages:[{role:'user',content:'x'}], tools:[{type:'function',function:{name:'a'}}], tool_choice:{type:'function',function:{name:'b'}}}}, expect: 400},
    // non-object tool parameters
    {envelope: {...envelope(), profile_id: FOO_AGENT_ID, profile_sha256: FOO_AGENT_SHA, body: {model:'foo-free', messages:[{role:'user',content:'x'}], tools:[{type:'function',function:{name:'a',parameters:[]}}]}}, expect: 400},
  ];
  for (const tc of cases) {
    const res = await worker(request(tc.envelope));
    expect(res.status).toBe(tc.expect);
    expect(res.headers.get('x-sub2api-dispatched')).toBe('false');
  }
  expect(calls).toBe(0);
});
