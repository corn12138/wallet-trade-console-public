const MAX_SAFE = BigInt(Number.MAX_SAFE_INTEGER);

function invalid(): never { throw new Error('invalid campaign JSON'); }

export function validWireString(value: string): boolean {
  // JSONB cannot represent NUL, and Go cannot preserve unpaired UTF-16 units.
  return !value.includes('\0') && !Array.from(value).some((point) =>
    point.length === 1 && point.charCodeAt(0) >= 0xd800 && point.charCodeAt(0) <= 0xdfff);
}

function exactInteger(raw: string): number {
  const parts = /^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(raw);
  if (!parts || raw.length > 128) invalid();
  const exponent = Number(parts[4] ?? 0);
  if (!Number.isInteger(exponent) || Math.abs(exponent) > 128) invalid();
  const fraction = parts[3] ?? '';
  let value = BigInt(parts[2] + fraction) * (parts[1] ? -1n : 1n);
  const scale = fraction.length - exponent;
  if (scale > 0) {
    const divisor = 10n ** BigInt(scale);
    if (value % divisor !== 0n) invalid();
    value /= divisor;
  } else value *= 10n ** BigInt(-scale);
  if (value > MAX_SAFE || value < -MAX_SAFE) invalid();
  return Number(value);
}

/** Parse before JSON.parse can erase duplicate decoded keys or round versions.
 * Every numeric field in this deliberately small contract is a safe integer. */
export function parseCampaignJSON(raw: string, limit: number): unknown {
  if (typeof raw !== 'string' || new TextEncoder().encode(raw).length > limit) invalid();
  let at = 0;
  function whitespace(): void { while (/[\x20\t\r\n]/.test(raw[at] ?? 'x')) at++; }
  function string(): string {
    const start = at++;
    while (at < raw.length) {
      const character = raw[at++];
      if (character === '\\') at++;
      else if (character === '"') {
        const value: unknown = JSON.parse(raw.slice(start, at));
        if (typeof value !== 'string' || !validWireString(value)) invalid();
        return value;
      }
    }
    return invalid();
  }
  function value(depth: number): unknown {
    if (depth > 24) invalid();
    whitespace();
    if (raw[at] === '"') return string();
    if (raw[at] === '{') {
      at++; whitespace();
      const result: Record<string, unknown> = Object.create(null);
      if (raw[at] === '}') { at++; return result; }
      while (at < raw.length) {
        whitespace();
        if (raw[at] !== '"') invalid();
        const key = string();
        if (Object.hasOwn(result, key)) invalid();
        whitespace();
        if (raw[at++] !== ':') invalid();
        result[key] = value(depth + 1);
        whitespace();
        const end = raw[at++];
        if (end === '}') return result;
        if (end !== ',') invalid();
      }
      return invalid();
    }
    if (raw[at] === '[') {
      at++; whitespace();
      const result: unknown[] = [];
      if (raw[at] === ']') { at++; return result; }
      while (at < raw.length) {
        if (result.length >= 50) invalid();
        result.push(value(depth + 1));
        whitespace();
        const end = raw[at++];
        if (end === ']') return result;
        if (end !== ',') invalid();
      }
      return invalid();
    }
    for (const [token, result] of [['true', true], ['false', false], ['null', null]] as const) {
      if (raw.startsWith(token, at)) { at += token.length; return result; }
    }
    const number = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(raw.slice(at));
    if (!number) invalid();
    at += number[0].length;
    return exactInteger(number[0]);
  }
  const parsed = value(0);
  whitespace();
  if (at !== raw.length) invalid();
  return parsed;
}
