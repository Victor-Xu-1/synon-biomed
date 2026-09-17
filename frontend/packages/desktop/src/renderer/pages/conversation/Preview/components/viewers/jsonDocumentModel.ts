export type JsonValue = null | boolean | number | string | JsonValue[] | { [key: string]: JsonValue };

export type JsonDocument =
  | { status: 'ready'; value: JsonValue; nodeCount: number }
  | { status: 'invalid' }
  | { status: 'too-large'; reason: 'characters' | 'nodes'; limit: number };

export const MAX_STRUCTURED_JSON_CHARS = 1_500_000;
export const MAX_STRUCTURED_JSON_NODES = 12_000;
export const MAX_EXPAND_ALL_JSON_NODES = 1_000;

export function parseJsonDocument(content: string): JsonDocument {
  if (content.length > MAX_STRUCTURED_JSON_CHARS) {
    return {
      status: 'too-large',
      reason: 'characters',
      limit: MAX_STRUCTURED_JSON_CHARS,
    };
  }

  let value: JsonValue;
  try {
    value = JSON.parse(content) as JsonValue;
  } catch {
    return { status: 'invalid' };
  }

  const stack: JsonValue[] = [value];
  let nodeCount = 0;
  while (stack.length > 0) {
    const current = stack.pop()!;
    nodeCount += 1;
    if (nodeCount > MAX_STRUCTURED_JSON_NODES) {
      return {
        status: 'too-large',
        reason: 'nodes',
        limit: MAX_STRUCTURED_JSON_NODES,
      };
    }
    if (Array.isArray(current)) stack.push(...current);
    else if (isJsonObject(current)) stack.push(...Object.values(current));
  }

  return { status: 'ready', value, nodeCount };
}

export function isJsonContainer(value: JsonValue): value is JsonValue[] | { [key: string]: JsonValue } {
  return Array.isArray(value) || isJsonObject(value);
}

export function jsonContainerSize(value: JsonValue[] | { [key: string]: JsonValue }): number {
  return Array.isArray(value) ? value.length : Object.keys(value).length;
}

export function describeJsonContainer(value: JsonValue[] | { [key: string]: JsonValue }): {
  kind: 'array' | 'object';
  size: number;
} {
  return { kind: Array.isArray(value) ? 'array' : 'object', size: jsonContainerSize(value) };
}

export function formatJsonScalar(value: Exclude<JsonValue, JsonValue[] | { [key: string]: JsonValue }>): string {
  if (value === null) return 'null';
  if (typeof value === 'string') return value;
  return String(value);
}

export function jsonScalarType(value: Exclude<JsonValue, JsonValue[] | { [key: string]: JsonValue }>): string {
  if (value === null) return 'null';
  if (typeof value === 'boolean') return 'boolean';
  if (typeof value === 'number') return 'number';
  return 'string';
}

export function jsonPath(parentPath: string, key: string): string {
  if (/^(0|[1-9]\d*)$/.test(key)) return `${parentPath}[${key}]`;
  if (/^[A-Za-z_$][\w$]*$/.test(key)) return `${parentPath}.${key}`;
  return `${parentPath}[${JSON.stringify(key)}]`;
}

export function jsonValueMatches(key: string, value: JsonValue, query: string): boolean {
  const normalized = query.trim().toLocaleLowerCase();
  if (!normalized) return true;
  if (key.toLocaleLowerCase().includes(normalized)) return true;
  if (!isJsonContainer(value) && formatJsonScalar(value).toLocaleLowerCase().includes(normalized)) return true;
  if (Array.isArray(value)) return value.some((child, index) => jsonValueMatches(String(index), child, normalized));
  if (isJsonObject(value))
    return Object.entries(value).some(([childKey, child]) => jsonValueMatches(childKey, child, normalized));
  return false;
}

export function collectJsonMatchPaths(value: JsonValue, query: string): Set<string> | null {
  const normalized = query.trim().toLocaleLowerCase();
  if (!normalized) return null;

  const matches = new Set<string>();
  const parents = new Map<string, string | null>([['$', null]]);
  const stack: Array<{ key: string; path: string; value: JsonValue }> = [{ key: '$', path: '$', value }];

  while (stack.length > 0) {
    const current = stack.pop()!;
    const keyMatches = current.key.toLocaleLowerCase().includes(normalized);
    const valueMatches = !isJsonContainer(current.value)
      ? formatJsonScalar(current.value).toLocaleLowerCase().includes(normalized)
      : false;

    if (keyMatches || valueMatches) {
      let cursor: string | null = current.path;
      while (cursor) {
        matches.add(cursor);
        cursor = parents.get(cursor) ?? null;
      }
    }

    if (isJsonContainer(current.value)) {
      Object.entries(current.value).forEach(([childKey, child]) => {
        const path = jsonPath(current.path, childKey);
        parents.set(path, current.path);
        stack.push({ key: Array.isArray(current.value) ? `[${childKey}]` : childKey, path, value: child });
      });
    }
  }

  return matches;
}

export function collectJsonContainerPaths(value: JsonValue, path = '$', target = new Set<string>()): Set<string> {
  if (!isJsonContainer(value)) return target;
  target.add(path);
  Object.entries(value).forEach(([key, child]) => collectJsonContainerPaths(child, jsonPath(path, key), target));
  return target;
}

function isJsonObject(value: JsonValue): value is { [key: string]: JsonValue } {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
