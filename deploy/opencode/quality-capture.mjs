// Diagnostic capture only. Importing this module never performs a request.
const URL_ALLOWED = 'https://opencode.ai/inference/openai/v1/chat/completions';
const MAX_BYTES = 2 * 1024 * 1024;
const HEADER_ALLOWLIST = ['content-type', 'content-length', 'retry-after', 'date', 'x-request-id', 'cf-ray'];
export function classify(text, expected) {
  return { exact_match: text === expected, optional_terminal_period: text === `${expected}.`, contains_expected: text.includes(expected), extra_text: text.includes(expected) && text !== expected && text !== `${expected}.` };
}
export function validateRequest(input) {
  if (!input || input.url !== URL_ALLOWED) throw new Error('URL is not the allowed endpoint');
  if (typeof input.body !== 'string' || typeof input.expected !== 'string' || !input.expected) throw new Error('body and nonempty expected must be strings');
  if (!Number.isInteger(input.timeout_ms) || input.timeout_ms < 1 || input.timeout_ms > 60000 || input.keepalive !== false) throw new Error('timeout_ms must be 1..60000 and keepalive must be false');
  if (!input.headers || typeof input.headers !== 'object' || Array.isArray(input.headers)) throw new Error('headers must be an object');
  for (const [key, value] of Object.entries(input.headers)) {
    if (typeof value !== 'string') throw new Error('header values must be strings');
    const k = key.toLowerCase();
    if (k === 'authorization' && value !== 'Bearer public') throw new Error('only Bearer public authorization is allowed');
    if (k !== 'authorization' && /(?:authorization|cookie|api[-_]?key|secret|token|credential)/i.test(k)) throw new Error('credential headers are not allowed');
  }
}
export function createSseParser(onEvent = () => {}) {
  const result = { text: '', reasoning: {}, role: null, finish_reason: null, tool_calls: [], events: [], chunks: [], upstream: { id: null, model: null, system_fingerprint: null }, usage: null, done: false, errors: [] };
  let line = '', afterCR = false, data = [], eventName = '', lastId = '', hasId = false;
  function dispatch() {
    if (!data.length) { eventName = ''; return; }
    const payload = data.join('\n');
    const event = { id: hasId ? lastId : null, event: eventName || 'message', data: payload };
    result.events.push(event); data = []; eventName = '';
    let hasContent = false;
    if (payload === '[DONE]') result.done = true;
    else {
      try {
        const item = JSON.parse(payload);
        result.chunks.push(item);
        if (!item || typeof item !== 'object' || Array.isArray(item)) throw new Error('JSON event is not an object');
        for (const key of ['id', 'model', 'system_fingerprint']) if (item[key] != null) result.upstream[key] = item[key];
        if (item.usage != null) result.usage = item.usage;
        if (item.error != null) result.errors.push({ type: 'upstream', detail: item.error });
        for (const choice of Array.isArray(item.choices) ? item.choices : []) {
          if (choice.index !== 0) continue;
          const delta = choice.delta ?? {};
          if (typeof delta.content === 'string') { result.text += delta.content; if (delta.content.length) hasContent = true; }
          if (delta.role != null) result.role = delta.role;
          for (const key of ['reasoning', 'reasoning_content', 'reasoning_details']) {
            if (delta[key] == null) continue;
            if (typeof delta[key] === 'string') result.reasoning[key] = (result.reasoning[key] ?? '') + delta[key];
            else (result.reasoning[key] ??= []).push(delta[key]);
          }
          if (Array.isArray(delta.tool_calls)) result.tool_calls.push(...delta.tool_calls);
          if (choice.finish_reason != null) result.finish_reason = choice.finish_reason;
        }
      } catch (error) { result.errors.push({ type: 'invalid_event', message: error.message, event_index: result.events.length - 1 }); }
    }
    onEvent({ hasContent, event, result });
  }
  function consumeLine(value) {
    if (!value) { dispatch(); return; }
    if (value.startsWith(':')) return;
    const colon = value.indexOf(':');
    const field = colon < 0 ? value : value.slice(0, colon);
    let val = colon < 0 ? '' : value.slice(colon + 1);
    if (val.startsWith(' ')) val = val.slice(1);
    if (field === 'data') data.push(val);
    else if (field === 'event') eventName = val;
    else if (field === 'id' && !val.includes('\0')) { lastId = val; hasId = true; }
  }
  return {
    result,
    feed(chunk) {
      for (const c of chunk) {
        if (afterCR) { afterCR = false; if (c === '\n') continue; }
        if (c === '\r' || c === '\n') { consumeLine(line); line = ''; if (c === '\r') afterCR = true; }
        else line += c;
      }
    },
    end() { /* Incomplete trailing events are not dispatched. */ },
  };
}
export function parseSse(raw) { const parser = createSseParser(); parser.feed(raw); parser.end(); return parser.result; }
export async function captureOnce(input) {
  validateRequest(input);
  if (typeof Bun === 'undefined' || Bun.version !== '1.4.2') throw new Error('Bun 1.4.2 is required');
  const start = performance.now();
  const elapsed = () => Math.round(performance.now() - start);
  let first_frame_ms = null, first_content_ms = null, raw = '', bytes = 0, reader = null;
  let status = null, headers = {}, failure = null, completeRead = false;
  const parser = createSseParser(({hasContent}) => { if (first_frame_ms === null) first_frame_ms = elapsed(); if (hasContent && first_content_ms === null) first_content_ms = elapsed(); });
  const decoder = new TextDecoder('utf-8');
  const validator = new TextDecoder('utf-8', {fatal: true});
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), input.timeout_ms);
  try {
    const response = await fetch(input.url, {method: 'POST', headers: input.headers, body: input.body, keepalive: false, redirect: 'error', signal: controller.signal});
    status = response.status;
    for (const name of HEADER_ALLOWLIST) { const value = response.headers.get(name); if (value !== null) headers[name] = value; }
    reader = response.body?.getReader() ?? null;
    if (reader) {
      while (true) {
        const part = await reader.read(); if (part.done) break;
        bytes += part.value.byteLength;
        if (bytes > MAX_BYTES) throw new Error('Response exceeds 2 MiB');
        validator.decode(part.value, {stream:true});
        const text = decoder.decode(part.value, {stream:true}); raw += text;
        if (status === 200) parser.feed(text);
      }
    }
    validator.decode();
    const tail = decoder.decode(); raw += tail; if (status === 200) { parser.feed(tail); parser.end(); }
    completeRead = true;
  } catch (error) { failure = { name: error.name, message: error.message }; }
  finally { clearTimeout(timer); if (reader) { await reader.cancel().catch(() => {}); reader.releaseLock(); } }
  const parsed = parser.result;
  const http_accepted = status === 200;
  if (!failure && !http_accepted) failure = { name: 'HttpError', message: `HTTP ${status}` };
  if (!failure && parsed.errors.length) failure = { name: 'SseError', message: 'SSE contained invalid or upstream error events' };
  const protocol_complete = http_accepted && completeRead && parsed.done && parsed.finish_reason !== null && !failure;
  return { runtime: `bun-${Bun.version}`, status, headers, raw, bytes, http_accepted, protocol_complete, ...classify(parsed.text, input.expected), text: parsed.text, reasoning: parsed.reasoning, role: parsed.role, finish_reason: parsed.finish_reason, tool_calls: parsed.tool_calls, events: parsed.events, chunks: parsed.chunks, upstream: parsed.upstream, usage: parsed.usage, done: parsed.done, errors: parsed.errors, error: failure, first_frame_ms, first_content_ms, elapsed_ms: elapsed() };
}
if (import.meta.main) {
  try { const input = JSON.parse(await Bun.stdin.text()); console.log(JSON.stringify(await captureOnce(input))); }
  catch (error) { console.log(JSON.stringify({http_accepted:false, protocol_complete:false, exact_match:false, error:{name:error.name,message:error.message}})); process.exitCode = 1; }
}
