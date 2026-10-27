import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';

const probe = new URL('./probe-free-chat.mjs', import.meta.url);
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
function build(flags) {
  const code = execFileSync(process.execPath, [probe.pathname, '--emit-script', ...flags], { encoding: 'utf8' });
  const match = code.match(/\n\}\)\((\{[\s\S]*\})\);\s*$/);
  assert.ok(match);
  return { code, request: JSON.parse(match[1]) };
}
const frame = value => `data: ${JSON.stringify(value)}\n\n`;
const content = value => frame({ choices: [{ index: 0, delta: { content: value } }] });
const finish = reason => frame({ choices: [{ index: 0, delta: {}, finish_reason: reason }] });
const terminal = finish('stop') + 'data: [DONE]\n\n';

for (const flags of [[], ['--minimal'], ['--balanced']]) {
  const name = flags[0] ?? 'native';
  const built = build(flags);
  const expected = built.request.expected;
  const cases = [
    { name: 'exact content', raw: content(expected) + terminal, success: true },
    { name: 'only choice zero', raw: frame({ choices: [{ index: 1, delta: { content: 'unrelated' } }, { index: 0, delta: { content: expected } }] }) + terminal, success: true },
    { name: 'multiline CRLF event', raw: `: heartbeat\r\ndata: {"choices":\r\ndata: [{"index":0,"delta":{"content":"${expected}"}}]}\r\n\r\n` + terminal, success: true },
    { name: 'extra text remains a failure', raw: content(`extra ${expected}`) + terminal, success: false },
    { name: 'upstream corruption is never corrected', raw: content('NONCE_Cpackage_1380') + terminal, success: false },
    { name: 'finish reason is required', raw: content(expected) + 'data: [DONE]\n\n', success: false },
    { name: 'length termination is not normal completion', raw: content(expected) + finish('length') + 'data: [DONE]\n\n', success: false },
    { name: 'incomplete terminator is not an event', raw: content(expected) + finish('stop') + 'data: [DONE]', success: false },
    { name: 'literal DONE in content is not termination', raw: content(`${expected}[DONE]`) + finish('stop'), success: false },
    { name: 'tool calls stay inert and fail validation', raw: content(expected) + frame({ choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { name: 'read', arguments: '{}' } }] } }] }) + terminal, success: false },
  ];
  for (const sample of cases) test(`${name}: ${sample.name}`, async () => {
    let result, calls = 0;
    const fakeProcess = {};
    const run = new AsyncFunction('Bun', 'fetch', 'console', 'process', built.code);
    await run({ version: '1.4.2' }, async (_url, options) => {
      calls += 1;
      assert.equal(options.method, 'POST');
      assert.equal(options.redirect, 'error');
      if (flags.length) assert.equal(options.keepalive, false);
      const bytes = new TextEncoder().encode(sample.raw);
      const body = new ReadableStream({ start(controller) {
        for (let i = 0; i < bytes.length; i += 3) controller.enqueue(bytes.slice(i, i + 3));
        controller.close();
      } });
      return new Response(body, { status: 200, headers: { 'content-type': 'text/event-stream' } });
    }, { log: value => { result = JSON.parse(value); } }, fakeProcess);
    assert.equal(calls, 1);
    assert.equal(result.success, sample.success);
    assert.equal(fakeProcess.exitCode ?? 0, sample.success ? 0 : 1);
    if (sample.name === 'only choice zero') assert.equal(result.text, expected);
    if (sample.name === 'upstream corruption is never corrected') assert.equal(result.text, 'NONCE_Cpackage_1380');
  });
}

test('balanced payload has its pinned generation contract', () => {
  const { request } = build(['--balanced']);
  const body = JSON.parse(request.body);
  assert.equal(request.profile_id, 'opencode-big-pickle-text-balanced-20261027-v2');
  assert.equal(body.temperature, 0);
  assert.equal(body.tool_choice, 'none');
  assert.equal(Buffer.byteLength(body.messages[0].content), 289);
  assert.deepEqual(Object.keys(request.headers).sort(), ['user-agent', 'x-session-id']);
});
