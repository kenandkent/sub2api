import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import {FREE_BUN_PROFILES} from './manifest.generated';

// Profile registry: the pinned table (generated from deploy/opencode/profiles/)
// plus dynamic derivation for admitted non-pinned free models. The substitution
// mirrors deploy/opencode/gen_profiles.py byte for byte so the derived id and
// SHA-256 always match what the Go gateway put in the envelope.

export const OPENCODE_FREE_ENDPOINT = 'https://opencode.ai/inference/openai/v1/chat/completions';

export type ChatMode = 'text' | 'agent';
export interface ResolvedProfile {
  mode: ChatMode;
  profile: any;
}

const DYNAMIC_STATUS = 'candidate-unvalidated';
const DYNAMIC_MODEL = /^[a-z0-9][a-z0-9.-]*-free$/;
const MAX_MODEL_LENGTH = 64;
const UNSUPPORTED_PREFIXES = ['jev-', 'gemini-'];
const TEMPLATE_MODEL = 'big-pickle';

const countOf = (text: string, needle: string) => text.split(needle).length - 1;
const hasOwn = (value: object, key: string) => Object.prototype.hasOwnProperty.call(value, key);

export function admissibleModel(model: string): boolean {
  if (hasOwn(FREE_BUN_PROFILES, model)) return true;
  if (model.length === 0 || model.length > MAX_MODEL_LENGTH || !DYNAMIC_MODEL.test(model)) return false;
  return !UNSUPPORTED_PREFIXES.some(prefix => model.startsWith(prefix));
}

export function createRegistry({profilesDir}: {profilesDir: string}): {resolve: (model: unknown, profileId: unknown, profileSha256: unknown) => ResolvedProfile | undefined} {
  const profiles: Record<string, any> = {};
  const templates: Record<string, Uint8Array> = {};
  for (const model of Object.keys(FREE_BUN_PROFILES) as (keyof typeof FREE_BUN_PROFILES)[]) {
    const entry = FREE_BUN_PROFILES[model];
    const specs: {mode: ChatMode, id: string, sha256: string, file: string}[] = [
      {mode: 'text', id: entry.id, sha256: entry.sha256, file: entry.file},
      {mode: 'agent', id: entry.agent.id, sha256: entry.agent.sha256, file: entry.agent.file},
    ];
    for (const spec of specs) {
      const bytes = readFileSync(join(profilesDir, spec.file));
      if (new Bun.CryptoHasher('sha256').update(bytes).digest('hex') !== spec.sha256) throw new Error('invalid profile hash');
      const profile = JSON.parse(bytes.toString());
      if (profile.id !== spec.id || profile.endpoint !== OPENCODE_FREE_ENDPOINT || profile.model !== model || profile.body?.model !== model) throw new Error('invalid profile');
      profiles[spec.id] = profile;
      if (model === TEMPLATE_MODEL) templates[spec.mode] = bytes;
    }
  }
  if (!templates.text || !templates.agent) throw new Error('missing profile templates');

  const substitute = (raw: Uint8Array, templateId: string, id: string, model: string): Uint8Array | undefined => {
    let text: string;
    try {
      text = new TextDecoder('utf-8', {fatal: true}).decode(raw);
    } catch {
      return undefined;
    }
    const quoted = `"${templateId}"`;
    if (countOf(text, quoted) !== 1) return undefined;
    text = text.replace(quoted, `"${id}"`);
    const statusKey = '"status": ';
    if (countOf(text, statusKey) !== 1) return undefined;
    const at = text.indexOf(statusKey);
    const tail = text.slice(at + statusKey.length);
    if (tail.length < 2) return undefined;
    const closing = tail.indexOf('"', 1);
    if (closing < 0) return undefined;
    text = text.slice(0, at) + statusKey + `"${DYNAMIC_STATUS}"` + tail.slice(closing + 1);
    if (countOf(text, TEMPLATE_MODEL) !== 2) return undefined;
    text = text.split(TEMPLATE_MODEL).join(model);
    return new TextEncoder().encode(text);
  };

  const derive = (model: string, mode: ChatMode): {id: string, sha256: string, profile: any} | undefined => {
    const template = mode === 'text' ? FREE_BUN_PROFILES[TEMPLATE_MODEL] : FREE_BUN_PROFILES[TEMPLATE_MODEL].agent;
    const id = `opencode-${model}-${mode}-balanced-20261027-v1`;
    const raw = substitute(templates[mode], template.id, id, model);
    if (!raw) return undefined;
    const sha256 = new Bun.CryptoHasher('sha256').update(raw).digest('hex');
    let profile: any;
    try {
      profile = JSON.parse(new TextDecoder('utf-8', {fatal: true}).decode(raw));
    } catch {
      return undefined;
    }
    if (profile.id !== id || profile.model !== model || profile.body?.model !== model) return undefined;
    return {id, sha256, profile};
  };

  const resolve = (model: unknown, profileId: unknown, profileSha256: unknown): ResolvedProfile | undefined => {
    if (typeof model !== 'string' || typeof profileId !== 'string' || typeof profileSha256 !== 'string') return undefined;
    if (hasOwn(FREE_BUN_PROFILES, model)) {
      const entry = (FREE_BUN_PROFILES as Record<string, any>)[model];
      // Pinned bytes are audited and differ from derivation; a pinned model
      // never accepts a derived identity.
      if (profileId === entry.id && profileSha256 === entry.sha256) return {mode: 'text', profile: profiles[entry.id]};
      if (profileId === entry.agent.id && profileSha256 === entry.agent.sha256) return {mode: 'agent', profile: profiles[entry.agent.id]};
      return undefined;
    }
    if (!admissibleModel(model)) return undefined;
    for (const mode of ['text', 'agent'] as const) {
      const derived = derive(model, mode);
      if (derived && derived.id === profileId && derived.sha256 === profileSha256) return {mode, profile: derived.profile};
    }
    return undefined;
  };

  return {resolve};
}
