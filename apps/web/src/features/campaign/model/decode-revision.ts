import schema from './schema.generated';
import type { CampaignContractMap, CampaignContractName, RevisionInput } from './contract.generated';
import { parseCampaignJSON, validWireString } from './decode-contract-json';

type Node = {
  readonly $ref?: string;
  readonly type?: string;
  readonly properties?: Readonly<Record<string, Node>>;
  readonly required?: readonly string[];
  readonly const?: string | number | boolean;
  readonly enum?: readonly string[];
  readonly minimum?: number;
  readonly maximum?: number;
  readonly minLength?: number;
  readonly maxLength?: number;
  readonly pattern?: string;
  readonly anyOf?: readonly Node[];
  readonly items?: Node;
  readonly minItems?: number;
  readonly maxItems?: number;
};

export type ContractDecode<T> =
  | { supported: true; value: T }
  | { supported: false; reason: 'invalid' | 'unsupported' };

export type RevisionDecode =
  | { supported: true; revision: RevisionInput }
  | { supported: false; reason: 'invalid' | 'unsupported' };

class InvalidRevision extends Error {
  constructor(readonly reason: 'invalid' | 'unsupported') { super(reason); }
}

function reject(reason: 'invalid' | 'unsupported' = 'invalid'): never {
  throw new InvalidRevision(reason);
}

type Definitions = Readonly<Record<string, Node>>;

function validate(value: unknown, node: Node, definitions: Definitions = schema.$defs): void {
  if (node.anyOf) {
    if (node.anyOf.length !== 2 || node.anyOf[1].type !== 'null') reject();
    if (value === null) return;
    return validate(value, node.anyOf[0], definitions);
  }
  if (node.$ref) {
    const target = node.$ref.startsWith('#/$defs/') && Object.hasOwn(definitions, node.$ref.slice(8)) && definitions[node.$ref.slice(8)];
    if (!target) reject();
    return validate(value, target, definitions);
  }
  switch (node.type) {
    case 'object': {
      if (!value || typeof value !== 'object' || Array.isArray(value)) reject();
      if (![Object.prototype, null].includes(Object.getPrototypeOf(value))) reject();
      const object = value as Record<string, unknown>;
      for (const key of node.required ?? []) {
        if (!Object.hasOwn(object, key)) reject();
      }
      for (const key of Object.keys(object)) {
        const field = node.properties?.[key];
        if (!field || !Object.hasOwn(node.properties ?? {}, key)) reject();
        validate(object[key], field, definitions);
      }
      break;
    }
    case 'array':
      if (!Array.isArray(value) || !node.items || node.maxItems === undefined || node.maxItems > 50 || value.length < (node.minItems ?? 0) || value.length > node.maxItems) reject();
      for (const item of value) validate(item, node.items, definitions);
      break;
    case 'boolean':
      if (typeof value !== 'boolean') reject();
      break;
    case 'integer':
      if (typeof value !== 'number' || !Number.isSafeInteger(value)) reject();
      if ((node.minimum !== undefined && value < node.minimum) || (node.maximum !== undefined && value > node.maximum)) reject();
      break;
    case 'string': {
      if (typeof value !== 'string' || !validWireString(value)) reject();
      const points = Array.from(value);
      if ((node.minLength !== undefined && points.length < node.minLength) || (node.maxLength !== undefined && points.length > node.maxLength)) reject();
      // JS's $ also matches before a trailing newline; Go's RE2 requires the
      // end of the value, so a partial match must not satisfy the wire format.
      if (node.pattern && new RegExp(node.pattern).exec(value)?.[0] !== value) reject();
      if (node.enum && !node.enum.includes(value)) reject('unsupported');
      break;
    }
    default: reject('unsupported');
  }
  if (node.const !== undefined && node.const !== value) reject('unsupported');
}

function failure(error: unknown): { supported: false; reason: 'invalid' | 'unsupported' } {
  return { supported: false, reason: error instanceof InvalidRevision ? error.reason : 'invalid' };
}

function contractLimit(name: CampaignContractName): number {
  return name.startsWith('Query') || name === 'CommandResponse' ? 1024 * 1024 : 64 * 1024;
}

/** Use the JSON entry point for transport bytes; an already parsed object no
 * longer contains evidence of duplicate keys or rounded numeric tokens. */
export function decodeCampaignContract<Name extends CampaignContractName>(name: Name, value: unknown): ContractDecode<CampaignContractMap[Name]> {
  try {
    if (!Object.hasOwn(schema.$defs, name)) reject();
    validate(value, schema.$defs[name]);
    const copy = JSON.stringify(value);
    if (new TextEncoder().encode(copy).length > contractLimit(name)) reject();
    return { supported: true, value: JSON.parse(copy) as CampaignContractMap[Name] };
  } catch (error) { return failure(error); }
}

export function decodeCampaignContractJSON<Name extends CampaignContractName>(name: Name, raw: string): ContractDecode<CampaignContractMap[Name]> {
  try { return decodeCampaignContract(name, parseCampaignJSON(raw, contractLimit(name))); }
  catch (error) { return failure(error); }
}

/** Decodes with another version of the campaign schema. Compatibility tests
 * use it to prove what a client built against that version accepts. */
export function decodeCampaignContractJSONUnder(other: { $defs: Definitions }, name: string, raw: string): ContractDecode<unknown> {
  try {
    if (!Object.hasOwn(other.$defs, name)) reject();
    const value = parseCampaignJSON(raw, 1024 * 1024);
    validate(value, other.$defs[name], other.$defs);
    return { supported: true, value };
  } catch (error) { return failure(error); }
}

function utcSeconds(value: string): number {
  const time = Date.parse(value);
  if (!Number.isFinite(time) || new Date(time).toISOString().replace('.000Z', 'Z') !== value) reject();
  return time;
}

/** This parser proves shape and display safety, never market availability or
 * permission to execute. Those remain authority checks in the target feature. */
export function decodeCampaignRevision(value: unknown): RevisionDecode {
  try {
    validate(value, schema.$defs.RevisionInput);
    // Copy the validated wire value so consumers cannot mutate a shared cache's
    // source while a publication is being compared with an accepted snapshot.
    const revision = JSON.parse(JSON.stringify(value)) as RevisionInput;
    const { materialTerms: terms, cta } = revision;
    if (utcSeconds(terms.endAt) <= utcSeconds(terms.startAt)) reject();
    for (const copy of [terms.copy.en, terms.copy.zh]) {
      for (const text of Object.values(copy)) {
        if (!text.trim() || /<[!/?a-zA-Z]/.test(text)) reject();
      }
    }
    if (cta.actionKind === 'none' && (cta.chainId !== undefined || cta.marketId !== undefined)) reject();
    if (cta.actionKind === 'openTrade' && (cta.chainId === undefined || cta.marketId === undefined)) reject();
    return { supported: true, revision };
  } catch (error) {
    return failure(error);
  }
}
