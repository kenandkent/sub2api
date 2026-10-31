import {timingSafeEqual} from 'node:crypto';
import {OPENCODE_FREE_ENDPOINT, createRegistry} from './profiles';
import {validateChatBody} from './chatBody';

const MAX_BODY = 1024 * 1024, MAX_RESPONSE = 8 * 1024 * 1024, MAX_EVENT = 256 * 1024, MAX_HEADERS = 16 * 1024, MAX_ACTIVE = 64, MAX_ERROR_BODY = 64 * 1024, MAX_ERROR_SNIPPET = 512;
const envelopeFields = new Set(['version','request_id','profile_id','profile_sha256','target','account_id','session_id','deadline_unix_ms','proxy','body']);
const isObject = (v: any) => v !== null && typeof v === 'object' && !Array.isArray(v);
const keysAllowed = (v: any, allowed: Set<string>) => Object.keys(v).every(k => allowed.has(k));
const isToken = (v: any, max = 128) => typeof v === 'string' && v.length > 0 && v.length <= max && /^[\x21-\x7e]+$/.test(v);

// Every dispatched failure path emits one structured line so docker logs can
// explain an opaque 502. Snippets are truncated and scrubbed of userinfo URLs
// and bearer tokens because proxy/edge junk bodies may embed credentials.
const redactSnippet = (text: string) => text
  .replace(/https?:\/\/[^\s"'@]+:[^\s"'@]+@/g, 'http://***:***@')
  .replace(/Bearer\s+[A-Za-z0-9._\-]+/g, 'Bearer ***')
  .slice(0, MAX_ERROR_SNIPPET);
const workerLog = (level: 'warn' | 'error', event: string, fields: Record<string, unknown>) => {
  try { console.error(JSON.stringify({level, event, component: 'opencode-free-worker', ts: new Date().toISOString(), ...fields})); } catch {}
};
// Proxy transport failures must not leak raw proxy/edge responses to callers,
// but upstream rejections (OpenCode console API JSON errors, Retry-After,
// x-request-id) have to keep their real status so the gateway can classify a
// free-quota 429 and cool the instance down.
const hasUpstreamEvidence = (headers: Headers, bodyComplete: boolean, bodyText: string) => {
  if (headers.get('retry-after') !== null || headers.get('x-request-id') !== null) return true;
  if (!bodyComplete || bodyText === '') return false;
  try {
    const parsed = JSON.parse(bodyText);
    if (!isObject(parsed)) return false;
    const err = parsed.error;
    if (typeof err === 'string') return err.length > 0;
    return isObject(err) && (typeof err.message === 'string' || typeof err.code === 'string');
  } catch { return false; }
};

export function createWorker({token, profilesDir, fetchImpl = fetch, bodyReadTimeoutMs = 5000}: {token: string, profilesDir: string, fetchImpl?: typeof fetch, bodyReadTimeoutMs?: number}) {
  if (typeof token !== 'string' || token.length < 32) throw new Error('invalid worker token');
  if (!Number.isInteger(bodyReadTimeoutMs) || bodyReadTimeoutMs < 1 || bodyReadTimeoutMs > 5000) throw new Error('invalid body timeout');
  const registry = createRegistry({profilesDir});
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
    if (active >= MAX_ACTIVE) {workerLog('warn','worker_busy',{active}); return error(503,'busy','',false);}
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
      const validEnvelope = isObject(data) && keysAllowed(data,envelopeFields) && data.version === 1 && isToken(data.request_id) && typeof data.profile_id === 'string' && typeof data.profile_sha256 === 'string' && data.target === 'console' && Number.isSafeInteger(data.account_id) && data.account_id > 0 && typeof data.session_id === 'string' && /^ses_[0-9a-f]{12}[a-zA-Z0-9]{14}$/.test(data.session_id) && Number.isSafeInteger(data.deadline_unix_ms);
      if (!validEnvelope) {finish(); return error(400,'invalid_envelope',id,false);}
      if (data.deadline_unix_ms <= Date.now() || data.deadline_unix_ms > Date.now()+3600000) {finish(); return error(400,'invalid_deadline',id,false);}
      const b = data.body;
      const resolved = registry.resolve(isObject(b) ? b.model : undefined, data.profile_id, data.profile_sha256);
      if (!resolved) {finish(); return error(400,'invalid_envelope',id,false);}
      if (!validateChatBody(b, resolved.mode)) {finish(); return error(400,'unsupported_request',id,false);}
      const profile = resolved.profile;
      let proxy: string | undefined;
      if (data.proxy !== undefined) {
        const p = data.proxy;
        if (!isObject(p) || !keysAllowed(p,new Set(['id','generation','url'])) || !Number.isSafeInteger(p.id) || p.id <= 0 || !isToken(p.generation) || typeof p.url !== 'string' || p.url.length > 2048) {finish(); return error(400,'invalid_proxy',id,false);}
        try {const parsed = new URL(p.url); if (parsed.protocol !== 'http:' || !parsed.hostname || parsed.pathname !== '/' || parsed.search || parsed.hash) throw new Error(); proxy=p.url;} catch {finish(); return error(400,'unsupported_proxy',id,false);}
      }
      const hasCallerTools = Array.isArray(b.tools) && b.tools.length > 0;
      // The free upstream inspects declared tool names and refuses any set that
      // lacks the official client's placeholder tool `read`. Caller tools are
      // preserved exactly and the placeholder is merged in when missing; the
      // gateway strips placeholder-only tool calls before they reach the caller.
      const readPlaceholder = {type:'function',function:{name:'read',description:'Do not call this tool. It exists only for API compatibility and must never be invoked.',parameters:{type:'object',properties:{filePath:{type:'string'}},required:['filePath']}}};
      const callerTools = hasCallerTools ? (b.tools.some((t:any)=>t?.function?.name==='read') ? b.tools : [...b.tools, readPlaceholder]) : profile.body.tools;
      const body = {...b, messages:[profile.body.messages[0], ...b.messages], tools:callerTools, tool_choice:hasCallerTools ? (b.tool_choice ?? 'auto') : 'none', stream:true, stream_options:{include_usage:true}, temperature:0};
      controller = new AbortController(); req.signal.addEventListener('abort',onAbort,{once:true});
      if (req.signal.aborted) {finish(); return error(499,'cancelled',id,false);}
      timer = setTimeout(() => controller?.abort(), data.deadline_unix_ms-Date.now());
      dispatched = true;
      const dispatchedAt = Date.now();
      const response = await fetchImpl(OPENCODE_FREE_ENDPOINT, {method:'POST', headers:{'user-agent':profile.headers['user-agent'],'x-session-id':data.session_id}, body:JSON.stringify(body), keepalive:false, redirect:'error', signal:controller.signal, ...(proxy ? {proxy} : {})} as any);
      if (proxy && response.status !== 200) {
        // Read a bounded prefix before deciding: upstream rejections must keep
        // their real status (quota 429 detection lives there), while proxy/edge
        // junk stays an opaque 502 with the evidence preserved in the log only.
        // Any read failure or oversized body keeps bodyComplete=false so the
        // conservative 502 path is chosen instead of forwarding a partial body.
        const chunks: Uint8Array[] = []; let sniffed = 0, bodyComplete = false;
        const sniffReader = response.body?.getReader();
        if (!sniffReader) {bodyComplete = true;}
        else {
          while (sniffed <= MAX_ERROR_BODY) {
            let result: ReadableStreamReadResult<Uint8Array>;
            try { result = await sniffReader.read(); } catch { break; }
            if (result.done) {bodyComplete = true; break;}
            chunks.push(result.value); sniffed += result.value.length;
          }
          if (!bodyComplete) void sniffReader.cancel().catch(()=>{});
        }
        const sniffedBytes = bodyComplete ? new Uint8Array(sniffed) : new Uint8Array(0);
        if (bodyComplete) {let offset = 0; for (const chunk of chunks) {sniffedBytes.set(chunk, offset); offset += chunk.length;}}
        const snippet = bodyComplete ? new TextDecoder().decode(sniffedBytes) : '';
        const upstreamish = bodyComplete && hasUpstreamEvidence(response.headers, true, snippet);
        workerLog('error','proxy_upstream_non_200',{
          request_id:id, account_id:data.account_id, profile_id:data.profile_id, model:b?.model,
          proxy_id:data.proxy?.id, proxy_generation:data.proxy?.generation,
          upstream_status:response.status,
          retry_after:response.headers.get('retry-after'),
          upstream_request_id:response.headers.get('x-request-id'),
          content_type:response.headers.get('content-type'),
          classified:bodyComplete ? (upstreamish ? 'upstream' : 'proxy_error') : 'proxy_error',
          body_truncated:!bodyComplete, body_bytes:sniffed,
          body_snippet:redactSnippet(snippet),
          dispatch_ms:Date.now()-dispatchedAt,
        });
        if (!upstreamish) {controller.abort(); finish(); return error(502,'proxy_or_upstream_http_error',id,true);}
        controller.abort(); finish();
        const passthrough = new Headers({'x-sub2api-transport-result':'upstream','x-sub2api-dispatched':'true','cache-control':'no-store'});
        for (const key of ['content-type','retry-after','x-request-id']) {const v=response.headers.get(key); if (v !== null) passthrough.set(key,v);}
        return new Response(sniffedBytes,{status:response.status,headers:passthrough});
      }
      let hsize=0; for (const [k,v] of response.headers) hsize+=k.length+v.length;
      if (hsize > MAX_HEADERS) {workerLog('error','upstream_headers_too_large',{request_id:id,account_id:data.account_id,header_bytes:hsize,dispatch_ms:Date.now()-dispatchedAt}); controller.abort(); finish(); return error(502,'upstream_headers_too_large',id,true);}
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
    } catch (e) {
      workerLog('error','dispatch_failed',{
        request_id:id, dispatched,
        error_name:e instanceof Error ? e.name : typeof e,
        error_message: redactSnippet(e instanceof Error ? e.message : String(e)),
      });
      controller?.abort(); finish(); return error(dispatched ? 502 : 400,dispatched ? 'upstream_error' : 'invalid_envelope',id,dispatched);
    }
  };
}
