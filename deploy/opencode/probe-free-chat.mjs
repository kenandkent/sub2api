#!/usr/bin/env node
// Diagnostic only: no tool executor, automatic retries, or proxy switching.
import { readFileSync } from 'node:fs';
import { createHash, randomBytes, randomInt } from 'node:crypto';

const args = process.argv.slice(2);
const send = args.includes('--send');
const emit = args.includes('--emit-script');
const zen = args.includes('--zen');
const minimal = args.includes('--minimal');
const balanced = args.includes('--balanced');
const compact = minimal || balanced;
if (args.includes('--help')) {
  console.log('Usage: bun deploy/opencode/probe-free-chat.mjs [--send | --emit-script] [--zen] [--minimal | --balanced]\nDefault: native-profile dry run. --minimal selects the small regression baseline; --balanced selects the evaluated low-token candidate with requested temperature 0. --send makes exactly one request with Bun 1.4.2; --emit-script prints a standalone Bun script for execution over SSH.');
  process.exit(0);
}
if (args.some(arg => !['--send', '--emit-script', '--zen', '--minimal', '--balanced'].includes(arg)) || (send && emit) || (minimal && balanced)) {
  throw new Error('Unknown or conflicting arguments; use --help.');
}
if (compact && zen) throw new Error('The compact profiles have only been tested on the Console Chat endpoint.');

// Same descending timestamp / counter / base62 layout as OpenCode session IDs.
const timestamp = ~(BigInt(Date.now()) * 0x1000n + 1n);
const timeHex = Array.from({ length: 6 }, (_, i) =>
  Number((timestamp >> BigInt(40 - i * 8)) & 0xffn).toString(16).padStart(2, '0'),
).join('');
const alphabet = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';
const session = `ses_${timeHex}${Array.from(randomBytes(14), b => alphabet[b % 62]).join('')}`;
const nonce = `NONCE_CAPTURE_${randomInt(1000, 10000)}`;
const template = readFileSync(new URL(balanced ? './balanced-text-profile.v2.json' : minimal ? './minimal-text-profile.v1.json' : './native-chat-template.v2.0.10.json', import.meta.url), 'utf8');
const profile = JSON.parse(template.replaceAll('__OPENCODE_SESSION_ID__', session).replaceAll('__OPENCODE_NONCE__', nonce));
const body = compact ? profile.body : profile;
const trace = randomBytes(16).toString('hex');
const span = randomBytes(8).toString('hex');
let headers = {
  authorization: 'Bearer public',
  'content-type': 'application/json',
  'user-agent': 'opencode/latest/2.0.10/cli',
  b3: `${trace}-${span}-1-${randomBytes(8).toString('hex')}`,
  traceparent: `00-${trace}-${span}-01`,
  'x-opencode-client': 'cli',
  'x-opencode-project': createHash('sha1').update('sub2api-opencode-diagnostic').digest('hex'),
  'x-opencode-session': session,
  'x-session-affinity': session,
  'x-session-id': session,
  connection: 'keep-alive',
  accept: '*/*',
  host: 'opencode.ai',
  'accept-encoding': 'gzip, deflate, br, zstd',
};
if (compact) headers = profile.headers;
const request = {
  url: compact ? profile.endpoint : zen ? 'https://opencode.ai/zen/v1/chat/completions' : 'https://opencode.ai/inference/openai/v1/chat/completions',
  headers,
  body: JSON.stringify(body),
  expected: nonce,
  profile_id: compact ? profile.id : 'native-opencode-2.0.10',
  profile_sha256: createHash('sha256').update(template).digest('hex'),
  ...(compact ? { keepalive: profile.fetch_options.keepalive } : {}),
};

async function performRequest(request) {
  if (typeof Bun === 'undefined' || Bun.version !== '1.4.2') throw new Error('Run the sending script with Bun 1.4.2; other runtimes are not validated.');
  const started = Date.now();
  const result = { timestamp: new Date().toISOString(), runtime: `bun-${Bun.version}`, url: request.url, expected: request.expected, profile_id: request.profile_id, profile_sha256: request.profile_sha256 };
  try {
    const response = await fetch(request.url, {
      method: 'POST', headers: request.headers, body: request.body,
      ...(request.keepalive === false ? { keepalive: false } : {}),
      redirect: 'error',
      signal: AbortSignal.timeout(60000),
    });
    const reader = response.body?.getReader();
    const decoder = new TextDecoder();
    let raw = '', bytes = 0;
    if (reader) {
      try {
        while (true) {
          const chunk = await reader.read();
          if (chunk.done) break;
          bytes += chunk.value.byteLength;
          if (bytes > 2 * 1024 * 1024) throw new Error('Diagnostic response exceeds 2 MiB.');
          raw += decoder.decode(chunk.value, { stream: true });
        }
        raw += decoder.decode();
      } finally {
        await reader.cancel().catch(() => {});
        reader.releaseLock();
      }
    }
    let text = '', usage = null, error = null, toolCallChunks = 0, done = false, finishReason = null;
    const normalized = raw.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
    const blocks = normalized.split('\n\n');
    blocks.pop(); // An unterminated trailing event is incomplete.
    for (const block of blocks) {
      const data = block.split('\n').filter(line => line === 'data' || line.startsWith('data:')).map(line => line === 'data' ? '' : line.slice(5).replace(/^ /, '')).join('\n');
      if (data === '[DONE]') { done = true; continue; }
      if (!data) continue;
      const item = JSON.parse(data);
      for (const choice of item.choices ?? []) {
        if (choice.index !== 0) continue;
        text += choice.delta?.content ?? '';
        if (choice.delta?.tool_calls?.length) toolCallChunks += 1;
        if (choice.finish_reason != null) finishReason = choice.finish_reason;
      }
      if (item.usage) usage = item.usage;
      if (item.error) error = item.error;
    }
    if (!response.ok) {
      try { error = JSON.parse(raw).error ?? JSON.parse(raw); }
      catch { error = { message: raw.slice(0, 2000) }; }
    }
    Object.assign(result, {
      status: response.status, elapsed_ms: Date.now() - started,
      retry_after: response.headers.get('retry-after'), text,
      matches: text.trim() === request.expected, exact_match: text === request.expected, done, finish_reason: finishReason,
      usage, error, tool_call_chunks: toolCallChunks,
      accepted: response.ok && done && finishReason === 'stop' && !error && !toolCallChunks,
      // Tool schemas reproduce the native request. Nothing executes them.
      success: response.ok && done && finishReason === 'stop' && text === request.expected && !error && !toolCallChunks,
    });
  } catch (error) {
    Object.assign(result, { success: false, elapsed_ms: Date.now() - started, error: { name: error.name, message: error.message } });
  }
  console.log(JSON.stringify(result, null, 2));
  if (!result.success) process.exitCode = 1;
}

if (emit) {
  console.log(`await (${performRequest.toString()})(${JSON.stringify(request)});`);
} else if (send) {
  await performRequest(request);
} else {
  console.log(JSON.stringify({
    dry_run: true, url: request.url, model: body.model,
    body_bytes: Buffer.byteLength(request.body), tools: body.tools.length,
    session, expected: nonce, authorization: headers.authorization ?? 'not explicitly set',
    profile_id: request.profile_id, header_names: Object.keys(headers),
    keepalive: request.keepalive ?? 'runtime-default',
    template_sha256: createHash('sha256').update(template).digest('hex'),
    note: 'No request sent. Use Bun 1.4.2 with --send, or --emit-script for SSH execution.',
  }, null, 2));
}
