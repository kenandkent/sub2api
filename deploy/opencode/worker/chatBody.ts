import type {ChatMode} from './profiles';

// Port of the audited chatSchema.ts text/agent request contract without zod.
// The Go gateway normalizes the body first; the worker revalidates it against
// the mode implied by the resolved profile identity and never rewrites caller
// content, tool definitions or history.

const TEXT_KEYS = new Set([
  'model', 'messages', 'temperature', 'n', 'stream', 'stream_options',
  'max_tokens', 'max_completion_tokens', 'stop', 'tools', 'tool_choice',
]);
const AGENT_KEYS = new Set([...TEXT_KEYS, 'parallel_tool_calls']);
const TOOL_NAME = /^[A-Za-z0-9_-]+$/;
const MAX_TOOLS = 128, MAX_TOOL_DEPTH = 64, MAX_TOOL_NODES = 100000, MAX_TOOL_NAME = 64;

const isObject = (v: any) => v !== null && typeof v === 'object' && !Array.isArray(v);
const keysAllowed = (v: any, allowed: Set<string>) => Object.keys(v).every(k => allowed.has(k));
const keysExact = (v: any, keys: string[]) => Object.keys(v).length === keys.length && keys.every(k => Object.prototype.hasOwnProperty.call(v, k));
const validToolName = (v: any): v is string => typeof v === 'string' && v.length > 0 && v.length <= MAX_TOOL_NAME && TOOL_NAME.test(v);

export function validateChatBody(body: any, mode: ChatMode): boolean {
  if (!isObject(body)) return false;
  if (!validToolParameters(body)) return false;
  return mode === 'text' ? validTextBody(body) : validAgentBody(body);
}

function validCommon(v: any): boolean {
  if (v.temperature !== undefined && v.temperature !== 0) return false;
  if (v.n !== undefined && v.n !== 1) return false;
  if (v.max_tokens !== undefined && !(Number.isInteger(v.max_tokens) && v.max_tokens > 0)) return false;
  if (v.max_completion_tokens !== undefined && !(Number.isInteger(v.max_completion_tokens) && v.max_completion_tokens > 0)) return false;
  if (v.stop !== undefined && !(typeof v.stop === 'string' || (Array.isArray(v.stop) && v.stop.every((item: any) => typeof item === 'string')))) return false;
  if (v.stream !== undefined && typeof v.stream !== 'boolean') return false;
  if (v.stream_options !== undefined) {
    const options = v.stream_options;
    if (!isObject(options) || !keysAllowed(options, new Set(['include_usage'])) || typeof options.include_usage !== 'boolean') return false;
  }
  return true;
}

function validTextBody(v: any): boolean {
  if (!keysAllowed(v, TEXT_KEYS)) return false;
  if (typeof v.model !== 'string') return false;
  if (!validTextMessages(v.messages)) return false;
  if (v.tools !== undefined && !(Array.isArray(v.tools) && v.tools.length === 0)) return false;
  if (v.tool_choice !== undefined && v.tool_choice !== 'none') return false;
  return validCommon(v);
}

function validTextMessages(messages: any): boolean {
  if (!Array.isArray(messages) || messages.length === 0) return false;
  for (const message of messages) {
    if (!isObject(message) || !keysExact(message, ['role', 'content'])) return false;
    if (message.role !== 'system' && message.role !== 'user' && message.role !== 'assistant') return false;
    if (typeof message.content !== 'string') return false;
  }
  return true;
}

function validAgentBody(v: any): boolean {
  if (!keysAllowed(v, AGENT_KEYS)) return false;
  if (typeof v.model !== 'string') return false;
  if (!Array.isArray(v.messages) || v.messages.length === 0) return false;
  const declared = new Set<string>();
  if (v.tools !== undefined) {
    if (!Array.isArray(v.tools) || v.tools.length > MAX_TOOLS) return false;
    for (const tool of v.tools) {
      const name = validToolDefinition(tool);
      if (name === undefined || declared.has(name)) return false;
      declared.add(name);
    }
  }
  if (v.tool_choice !== undefined && !validToolChoice(v.tool_choice, declared)) return false;
  if (v.parallel_tool_calls !== undefined && typeof v.parallel_tool_calls !== 'boolean') return false;
  if (!validAgentMessages(v.messages)) return false;
  return validCommon(v);
}

function validToolDefinition(tool: any): string | undefined {
  if (!isObject(tool) || !keysExact(tool, ['type', 'function'])) return undefined;
  if (tool.type !== 'function') return undefined;
  const fn = tool.function;
  if (!isObject(fn)) return undefined;
  for (const key of Object.keys(fn)) {
    if (key !== 'name' && key !== 'description' && key !== 'parameters' && key !== 'strict') return undefined;
  }
  if (!validToolName(fn.name)) return undefined;
  if (fn.description !== undefined && typeof fn.description !== 'string') return undefined;
  if (fn.strict !== undefined && fn.strict !== null && typeof fn.strict !== 'boolean') return undefined;
  if (fn.parameters !== undefined && !isObject(fn.parameters)) return undefined;
  return fn.name;
}

function validToolChoice(choice: any, declared: Set<string>): boolean {
  if (typeof choice === 'string') {
    if (choice !== 'auto' && choice !== 'none' && choice !== 'required') return false;
    if (choice === 'required' && declared.size === 0) return false;
    return true;
  }
  if (!isObject(choice) || !keysExact(choice, ['type', 'function'])) return false;
  if (choice.type !== 'function') return false;
  const fn = choice.function;
  if (!isObject(fn) || !keysExact(fn, ['name'])) return false;
  if (!validToolName(fn.name)) return false;
  return declared.has(fn.name);
}

function validAgentContent(content: any): boolean {
  if (typeof content === 'string') return true;
  if (!Array.isArray(content)) return false;
  for (const part of content) {
    if (!isObject(part) || !keysExact(part, ['type', 'text'])) return false;
    if (part.type !== 'text' || typeof part.text !== 'string') return false;
  }
  return true;
}

function validAgentMessages(messages: any[]): boolean {
  const pending = new Set<string>();
  const seen = new Set<string>();
  for (const message of messages) {
    if (!isObject(message) || typeof message.role !== 'string') return false;
    if (message.role === 'tool') {
      if (!keysAllowed(message, new Set(['role', 'content', 'tool_call_id', 'name']))) return false;
      if (!validAgentContent(message.content)) return false;
      if (typeof message.tool_call_id !== 'string' || message.tool_call_id === '') return false;
      if (message.name !== undefined && !validToolName(message.name)) return false;
      if (!pending.delete(message.tool_call_id)) return false;
      continue;
    }
    if (pending.size > 0) return false;
    if (message.role === 'system' || message.role === 'user') {
      if (!keysAllowed(message, new Set(['role', 'content', 'name']))) return false;
      if (!validAgentContent(message.content)) return false;
      if (message.name !== undefined && !validToolName(message.name)) return false;
      continue;
    }
    if (message.role !== 'assistant') return false;
    if (!keysAllowed(message, new Set(['role', 'content', 'name', 'reasoning_content', 'reasoning', 'tool_calls']))) return false;
    if (message.name !== undefined && !validToolName(message.name)) return false;
    if (message.reasoning_content !== undefined && typeof message.reasoning_content !== 'string') return false;
    if (message.reasoning !== undefined && typeof message.reasoning !== 'string') return false;
    let hasContent = false;
    if (message.content !== undefined && message.content !== null) {
      if (!validAgentContent(message.content)) return false;
      hasContent = true;
    }
    const calls = message.tool_calls;
    if (calls !== undefined && (!Array.isArray(calls) || calls.length === 0)) return false;
    if (!hasContent && (calls === undefined || calls.length === 0)) return false;
    for (const call of calls ?? []) {
      if (!isObject(call) || !keysExact(call, ['id', 'type', 'function'])) return false;
      if (typeof call.id !== 'string' || call.id === '') return false;
      if (call.type !== 'function') return false;
      const fn = call.function;
      if (!isObject(fn) || !keysExact(fn, ['name', 'arguments'])) return false;
      if (!validToolName(fn.name) || typeof fn.arguments !== 'string') return false;
      if (seen.has(call.id)) return false;
      seen.add(call.id);
      pending.add(call.id);
    }
  }
  return pending.size === 0;
}

// Bounded, acyclic-shape walk over caller tool schemas. The input came from
// JSON.parse, so cycles and exotic prototypes cannot occur; depth and node
// budgets bound parsing work exactly like the Go gateway's walk.
function validToolParameters(v: any): boolean {
  if (!isObject(v) || !Array.isArray(v.tools)) return true;
  if (v.tools.length > MAX_TOOLS) return false;
  let nodes = 0;
  const walk = (value: any, depth: number): boolean => {
    if (++nodes > MAX_TOOL_NODES || depth > MAX_TOOL_DEPTH) return false;
    if (value === null || typeof value === 'string' || typeof value === 'boolean') return true;
    if (typeof value === 'number') return Number.isFinite(value);
    if (Array.isArray(value)) {
      for (const item of value) if (!walk(item, depth + 1)) return false;
      return true;
    }
    if (!isObject(value)) return false;
    const prototype = Object.getPrototypeOf(value);
    if (prototype !== Object.prototype && prototype !== null) return false;
    for (const key of Object.keys(value)) if (!walk(value[key], depth + 1)) return false;
    return true;
  };
  for (const tool of v.tools) {
    if (!isObject(tool) || !isObject(tool.function) || tool.function.parameters === undefined) continue;
    if (!walk(tool.function.parameters, 1)) return false;
  }
  return true;
}
