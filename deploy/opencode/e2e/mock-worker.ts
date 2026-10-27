// Test-only factory injection: the production worker has no endpoint override.
import {createWorker} from '../worker/worker';
import {fileURLToPath} from 'node:url';

const profilePath = fileURLToPath(new URL('../balanced-text-profile.v2.json', import.meta.url));
const token = process.env.OPENCODE_FREE_BUN_TOKEN || '';
const encode = new TextEncoder();
const event = (value: unknown) => `data: ${typeof value === 'string' ? value : JSON.stringify(value)}\n\n`;
const fetchImpl: typeof fetch = async (url, init) => {
  if (url !== 'https://opencode.ai/inference/openai/v1/chat/completions') throw new Error('unexpected destination');
  const body = JSON.parse(String(init?.body));
  const mode = body.messages.at(-1)?.content;
  const prefix = event({id:'mock',object:'chat.completion.chunk',model:'big-pickle',choices:[{index:0,delta:{role:'assistant',content:'fixture response'},finish_reason:null}]});
  const finish = event({id:'mock',object:'chat.completion.chunk',model:'big-pickle',choices:[{index:0,delta:{},finish_reason:'stop'}]});
  const usage = event({id:'mock',object:'chat.completion.chunk',model:'big-pickle',choices:[],usage:{prompt_tokens:10,completion_tokens:2,total_tokens:12,prompt_tokens_details:{cached_tokens:3},completion_tokens_details:{reasoning_tokens:0},cost:0,opencode:{billing_source:'free',list_cost:0,tokens:{input:7,output:2,reasoning:0,cache_read:3,cache_write_5m:0,cache_write_1h:0}}}});
  if (mode === 'cancel') {
    return new Response(new ReadableStream({start(controller) {
      controller.enqueue(encode.encode(prefix));
      const timer = setTimeout(() => { try {controller.enqueue(encode.encode(finish + usage + event('[DONE]'))); controller.close();} catch {} }, 5000);
      init?.signal?.addEventListener('abort', () => {clearTimeout(timer); try {controller.close();} catch {}}, {once:true});
    }}), {headers:{'content-type':'text/event-stream'}});
  }
  return new Response(prefix + finish + (mode === 'missing-usage' ? '' : usage) + event('[DONE]'), {headers:{'content-type':'text/event-stream'}});
};
const worker = createWorker({token,profilePath,fetchImpl});
Bun.serve({hostname:'127.0.0.1',port:19090,idleTimeout:120,fetch(req) {
  if (req.method === 'GET' && new URL(req.url).pathname === '/health/e2e') return new Response('test-only mock');
  return worker(req);
}});
