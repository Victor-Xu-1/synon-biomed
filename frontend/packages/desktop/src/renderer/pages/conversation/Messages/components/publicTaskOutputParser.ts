const MAX_SOURCE_CHARACTERS = 512_000;
const MAX_DEPTH = 32;

export function collectEmbeddedTaskStructures(value: Record<string, unknown>): Record<string, unknown>[] {
  const results: Record<string, unknown>[] = [];
  const seen = new Set<string>();
  for (const key of ['stdout', 'preview', 'content', 'output']) {
    const candidate = value[key];
    if (typeof candidate !== 'string') continue;
    const parsed = parseTaskStructuredText(candidate);
    if (!isRecord(parsed)) continue;
    const signature = stableSignature(parsed);
    if (seen.has(signature)) continue;
    seen.add(signature);
    results.push(parsed);
    for (const nested of collectEmbeddedTaskStructures(parsed)) {
      const nestedSignature = stableSignature(nested);
      if (seen.has(nestedSignature)) continue;
      seen.add(nestedSignature);
      results.push(nested);
    }
  }
  return results;
}

export function parseTaskStructuredText(value: string): unknown | null {
  const source = value.trim().slice(0, MAX_SOURCE_CHARACTERS);
  if (!source) return null;
  for (const start of candidateStarts(source)) {
    const candidate = source.slice(start);
    try {
      return JSON.parse(candidate);
    } catch {
      try {
        return new PythonLiteralParser(candidate).parse();
      } catch {
        // Try the next balanced-looking structure in the task output.
      }
    }
  }
  return null;
}

function candidateStarts(value: string): number[] {
  const starts = [0];
  for (let index = 0; index < value.length && starts.length < 16; index += 1) {
    if ((value[index] === '{' || value[index] === '[') && index !== 0) starts.push(index);
  }
  return [...new Set(starts)];
}

class PythonLiteralParser {
  private position = 0;

  constructor(private readonly source: string) {}

  parse(): unknown {
    const value = this.parseValue(0);
    return value;
  }

  private parseValue(depth: number): unknown {
    if (depth > MAX_DEPTH) throw new Error('task output nesting is too deep');
    this.skipWhitespace();
    const character = this.source[this.position];
    if (character === '{') return this.parseObject(depth + 1);
    if (character === '[' || character === '(') return this.parseArray(depth + 1, character === '(' ? ')' : ']');
    if (character === "'" || character === '"') return this.parseString(character);
    if (character === '-' || /[0-9]/u.test(character ?? '')) return this.parseNumber();
    return this.parseIdentifier();
  }

  private parseObject(depth: number): Record<string, unknown> {
    const result: Record<string, unknown> = {};
    this.expect('{');
    this.skipWhitespace();
    while (this.source[this.position] !== '}') {
      const rawKey = this.parseValue(depth);
      const key = typeof rawKey === 'string' || typeof rawKey === 'number' ? String(rawKey) : '';
      if (!key) throw new Error('unsupported task output key');
      this.skipWhitespace();
      this.expect(':');
      result[key] = this.parseValue(depth);
      this.skipWhitespace();
      if (this.source[this.position] === ',') {
        this.position += 1;
        this.skipWhitespace();
        if (this.source[this.position] === '}') break;
        continue;
      }
      break;
    }
    this.expect('}');
    return result;
  }

  private parseArray(depth: number, closing: ']' | ')'): unknown[] {
    const result: unknown[] = [];
    this.position += 1;
    this.skipWhitespace();
    while (this.source[this.position] !== closing) {
      result.push(this.parseValue(depth));
      this.skipWhitespace();
      if (this.source[this.position] === ',') {
        this.position += 1;
        this.skipWhitespace();
        if (this.source[this.position] === closing) break;
        continue;
      }
      break;
    }
    this.expect(closing);
    return result;
  }

  private parseString(quote: string): string {
    this.position += 1;
    let result = '';
    while (this.position < this.source.length) {
      const character = this.source[this.position];
      this.position += 1;
      if (character === quote) return result;
      if (character !== '\\') {
        result += character;
        continue;
      }
      const escaped = this.source[this.position];
      this.position += 1;
      const escapes: Record<string, string> = { n: '\n', r: '\r', t: '\t', '\\': '\\', "'": "'", '"': '"' };
      result += escapes[escaped] ?? escaped;
    }
    throw new Error('unterminated task output string');
  }

  private parseNumber(): number {
    const match = this.source.slice(this.position).match(/^-?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?/u);
    if (!match) throw new Error('invalid task output number');
    this.position += match[0].length;
    return Number(match[0]);
  }

  private parseIdentifier(): unknown {
    const match = this.source.slice(this.position).match(/^[A-Za-z_][A-Za-z0-9_]*/u);
    if (!match) throw new Error('invalid task output literal');
    this.position += match[0].length;
    if (match[0] === 'True') return true;
    if (match[0] === 'False') return false;
    if (match[0] === 'None') return null;
    throw new Error('unsupported task output literal');
  }

  private skipWhitespace() {
    while (/\s/u.test(this.source[this.position] ?? '')) this.position += 1;
  }

  private expect(character: string) {
    this.skipWhitespace();
    if (this.source[this.position] !== character) throw new Error(`expected ${character}`);
    this.position += 1;
  }
}

function stableSignature(value: Record<string, unknown>): string {
  try {
    return JSON.stringify(value).slice(0, 2048);
  } catch {
    return Object.keys(value).toSorted().join('|');
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value));
}
