import {createWorker} from './worker';
import {fileURLToPath} from 'node:url';

// Bun's fetch honors ambient proxy variables, including for requests with no
// explicit proxy. Remove them once at startup; per-request proxy settings are
// passed directly to fetch and never change the process environment.
for (const name of Object.keys(process.env)) {
  if (/^(http_proxy|https_proxy|all_proxy|no_proxy)$/i.test(name)) delete process.env[name];
}
if (Bun.version !== '1.4.2') throw new Error('worker requires Bun 1.4.2');
const token = process.env.OPENCODE_FREE_BUN_TOKEN || '';
const host = process.env.LISTEN_HOST || '127.0.0.1';
if (host !== '127.0.0.1' && host !== '::1') throw new Error('worker must listen on loopback');
const port = Number(process.env.PORT || '19090');
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('invalid port');
const profilesDir = process.env.PROFILES_DIR || fileURLToPath(new URL('../profiles', import.meta.url));
Bun.serve({hostname:host, port, idleTimeout: 120, fetch:createWorker({token,profilesDir})});
