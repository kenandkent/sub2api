import {readFileSync} from 'node:fs';
import {timingSafeEqual} from 'node:crypto';

const PROFILE_ID = 'opencode-big-pickle-text-balanced-20261027-v2';
const PROFILE_HASH = 'da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9';
const ENDPOINT = 'https://opencode.ai/inference/openai/v1/chat/completions';
const MAX_BODY = 1024 * 1024, MAX_RESPONSE = 8 * 1024 * 1024, MAX_EVENT = 256 * 1024, MAX_HEADERS = 16 * 1024, MAX_ACTIVE = 64;
const bodyFields = new Set(['model','messages','temperature','tools','tool_choice','n','stream','stream_options']);
const envelopeFields = new Set(['version','request_id','profile_id','profile_sha256','target','account_id','session_id','deadline_unix_ms','proxy','body']);
const isObject = (v: any) => v !== null && typeof v === 'object' && !Array.isArray(v);
const keysAllowed = (v: any, allowed: Set<string>) => Object.keys(v).every(k => allowed.has(k));
const isToken = (v: any, max = 128) => typeof v === 'string' && v.length > 0 && v.length <= max && /^[\x21-\x7e]+$/.test(v);

export function createWorker({token, profilePath, fetchImpl = fetch, bodyReadTimeoutMs = 5000}: {token: string, profilePath: string, fetchImpl?: typeof fetch, bodyReadTimeoutMs?: number}) {
  if (typeof token !== 'string' || token.length < 32) throw new Error('invalid worker token');
  if (!Number.isInteger(bodyReadTimeoutMs) || bodyReadTimeoutMs < 1 || bodyReadTimeoutMs > 5000) throw new Error('invalid body timeout');
  const bytes = readFileSync(profilePath);
  if (new Bun.CryptoHasher('sha256').update(bytes).digest('hex') !== PROFILE_HASH) throw new Error('invalid profile hash');
  const profile = JSON.parse(bytes.toString());
  if (profile.id !== PROFILE_ID || profile.endpoint !== ENDPOINT || profile.model !== 'big-pickle') throw new Error('invalid profile');
  let active = 0;
  function error(status: number, code: string, requestId: string, dispatched: boolean): Response {
    return Response.json({error:{code,message:code},request_id:requestId,dispatched}, {status, headers:{'x-sub2api-transport-result':'worker-error','x-sub2api-dispatched':String(dispatched),'cache-control':'no-store'}});
  }
  return async function worker(req: Request): Promise<Response> {
    const path = new URL(req.url).pathname;
    if (req.method === 'GET' && (path === '/health/live' || path === '/health/ready')) return Response.json({ok:true});
    if (req.method !== 'POST' || path !== '/v1/transport/chat') return error(404,'not_found','',false);
    const auth = req.headers.get('authorization') || '';
    const expected = Buffer.from(`Bearer ${token}`), actual = Buffer.from(auth);
    if (actual.length !== expected.length || !timingSafeEqual(actual, expected)) return error(401,'unauthorized','',false);
    let headerBytes = 0; for (const [k,v] of req.headers) headerBytes += k.length + v.length;
    if (headerBytes > MAX_HEADERS) return error(431,'headers_too_large','',false);
    if (active >= MAX_ACTIVE) return error(503,'busy','',false);
    active++;
    let released = false; const release = () => {if (!released) {released = true; active--;}};
    let id = '', dispatched = false;
    let controller: AbortController | undefined, timer: ReturnType<typeof setTimeout> | undefined, streamAbort: (() => void) | undefined;
    const finish = () => {if (timer) clearTimeout(timer); req.signal.removeEventListener('abort', onAbort); if (streamAbort) controller?.signal.removeEventListener('abort',streamAbort); release();};
    const onAbort = () => controller?.abort();
    try {
      const reader = req.body?.getReader();
      if (!reader) {finish(); return error(400,'invalid_envelope',id,false);}
      const chunks: Uint8Array[] = []; let length = 0;
      const bodyTimeout = Symbol('body timeout'), bodyCancelled = Symbol('body cancelled');
      let bodyTimer: ReturnType<typeof setTimeout>; let cancelBody: () => void = () => {};
      const interruption = new Promise<never>((_,reject) => {
        bodyTimer = setTimeout(() => reject(bodyTimeout), bodyReadTimeoutMs);
        cancelBody = () => reject(bodyCancelled);
        req.signal.addEventListener('abort', cancelBody, {once:true});
        if (req.signal.aborted) cancelBody();
      });
      try {
        while (true) {const {value,done} = await Promise.race([reader.read(),interruption]); if (done) break; length += value.length; if (length > MAX_BODY) {void reader.cancel().catch(()=>{}); finish(); return error(413,'body_too_large',id,false);} chunks.push(value);}
      } catch (cause) {
        void reader.cancel().catch(()=>{}); finish();
        return error(cause === bodyTimeout ? 408 : cause === bodyCancelled ? 499 : 400, cause === bodyTimeout ? 'body_timeout' : cause === bodyCancelled ? 'cancelled' : 'invalid_envelope',id,false);
      } finally {clearTimeout(bodyTimer!); req.signal.removeEventListener('abort',cancelBody);}
      const buf = new Uint8Array(length); let offset=0; for (const chunk of chunks) {buf.set(chunk,offset); offset+=chunk.length;}
      let data: any; try {data = JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(buf));} catch {finish(); return error(400,'invalid_envelope',id,false);}
      if (isObject(data) && isToken(data.request_id)) id = data.request_id;
      const validEnvelope = isObject(data) && keysAllowed(data,envelopeFields) && data.version === 1 && isToken(data.request_id) && data.profile_id === PROFILE_ID && data.profile_sha256 === PROFILE_HASH && data.target === 'console' && Number.isSafeInteger(data.account_id) && data.account_id > 0 && typeof data.session_id === 'string' && /^ses_[0-9a-f]{12}[a-zA-Z0-9]{14}$/.test(data.session_id) && Number.isSafeInteger(data.deadline_unix_ms);
      if (!validEnvelope) {finish(); return error(400,'invalid_envelope',id,false);}
      if (data.deadline_unix_ms <= Date.now() || data.deadline_unix_ms > Date.now()+3600000) {finish(); return error(400,'invalid_deadline',id,false);}
      const b=data.body;
      const validBody = isObject(b) && keysAllowed(b,bodyFields) && b.model === 'big-pickle' && Array.isArray(b.messages) && b.messages.length > 0 && b.messages.every((m: any) => isObject(m) && keysAllowed(m,new Set(['role','content'])) && ['system','user','assistant'].includes(m.role) && typeof m.content === 'string') && (b.temperature === undefined || b.temperature === 0) && (b.tools === undefined || (Array.isArray(b.tools) && b.tools.length === 0)) && (b.tool_choice === undefined || b.tool_choice === 'none') && (b.n === undefined || b.n === 1) && (b.stream === undefined || typeof b.stream === 'boolean') && (b.stream_options === undefined || (isObject(b.stream_options) && keysAllowed(b.stream_options,new Set(['include_usage'])) && typeof b.stream_options.include_usage === 'boolean'));
      if (!validBody) {finish(); return error(400,'unsupported_request',id,false);}
      let proxy: string | undefined;
      if (data.proxy !== undefined) {
        const p = data.proxy;
        if (!isObject(p) || !keysAllowed(p,new Set(['id','generation','url'])) || !Number.isSafeInteger(p.id) || p.id <= 0 || !isToken(p.generation) || typeof p.url !== 'string' || p.url.length > 2048) {finish(); return error(400,'invalid_proxy',id,false);}
        try {const parsed = new URL(p.url); if (parsed.protocol !== 'http:' || !parsed.hostname || parsed.pathname !== '/' || parsed.search || parsed.hash) throw new Error(); proxy=p.url;} catch {finish(); return error(400,'unsupported_proxy',id,false);}
      }
      const body = {model:'big-pickle', messages:[profile.body.messages[0], ...b.messages], tools:profile.body.tools, tool_choice:'none', stream:true, stream_options:{include_usage:true}, temperature:0};
      controller = new AbortController(); req.signal.addEventListener('abort',onAbort,{once:true});
      if (req.signal.aborted) {finish(); return error(499,'cancelled',id,false);}
      timer = setTimeout(() => controller?.abort(), data.deadline_unix_ms-Date.now());
      dispatched = true;
      const response = await fetchImpl(ENDPOINT, {method:'POST', headers:{'user-agent':profile.headers['user-agent'],'x-session-id':data.session_id}, body:JSON.stringify(body), keepalive:false, redirect:'error', signal:controller.signal, ...(proxy ? {proxy} : {})} as any);
      if (proxy && response.status !== 200) {controller.abort(); void response.body?.cancel().catch(()=>{}); finish(); return error(502,'proxy_or_upstream_http_error',id,true);}
      let hsize=0; for (const [k,v] of response.headers) hsize+=k.length+v.length;
      if (hsize > MAX_HEADERS) {controller.abort(); finish(); return error(502,'upstream_headers_too_large',id,true);}
      const headers = new Headers({'x-sub2api-transport-result':'upstream','x-sub2api-dispatched':'true','cache-control':'no-store'});
      for (const key of ['content-type','retry-after','x-request-id']) {const v=response.headers.get(key); if (v !== null) headers.set(key,v);}
      if (!response.body) {finish(); return new Response(null,{status:response.status,headers});}
      const upstream = response.body.getReader(); let total=0, event=0, line=0, terminated=false;
      const stream = new ReadableStream<Uint8Array>({
        start(out) {
          streamAbort = () => {if (terminated) return; terminated=true; void upstream.cancel().catch(()=>{}); finish(); out.error(new Error('upstream aborted'));};
          controller!.signal.addEventListener('abort',streamAbort,{once:true});
          if (controller!.signal.aborted) streamAbort();
        },
        async pull(out) {
          try {
            const {value,done} = await upstream.read();
            if (terminated) return;
            if (done) {terminated=true; finish(); out.close(); return;}
            total+=value.length;
            for (const byte of value) {event++; if (event > MAX_EVENT) throw new Error('event too large'); if (byte === 10) {if (line === 0) event=0; line=0;} else if (byte !== 13) line++;}
            if (total > MAX_RESPONSE) throw new Error('response too large');
            out.enqueue(value);
          } catch (e) {if (!terminated) {terminated=true; controller?.abort(); void upstream.cancel().catch(()=>{}); finish(); out.error(e);}}
        },
        async cancel() {terminated=true; controller?.abort(); try {await upstream.cancel();} catch {} finally {finish();}}
      }, {highWaterMark:0});
      return new Response(stream,{status:response.status,headers});
    } catch {controller?.abort(); finish(); return error(dispatched ? 502 : 400,dispatched ? 'upstream_error' : 'invalid_envelope',id,dispatched);}
  };
}
