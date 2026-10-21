/** 原生 JSON 结构的安全编辑工具：路径写入不丢未知字段。 */

export type JsonObject = Record<string, unknown>

export function isRecord(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function cloneJson<T>(value: T): T {
  if (typeof structuredClone === 'function') return structuredClone(value)
  return JSON.parse(JSON.stringify(value)) as T
}

export function getPath(source: unknown, path: string): unknown {
  let current: unknown = source
  for (const key of path.split('.')) {
    if (!isRecord(current)) return undefined
    current = current[key]
  }
  return current
}

/**
 * 不可变写入路径。value === undefined 表示删除该键（显式清理）。
 * 其余键（包括结构化编辑器不认识的键）原样保留。
 */
export function setPath<T extends JsonObject>(source: T, path: string, value: unknown): T {
  const next = cloneJson(source)
  const keys = path.split('.')
  let cursor: JsonObject = next
  for (let i = 0; i < keys.length - 1; i += 1) {
    const key = keys[i]!
    const child = cursor[key]
    if (!isRecord(child)) cursor[key] = {}
    cursor = cursor[key] as JsonObject
  }
  const last = keys[keys.length - 1]!
  if (value === undefined) delete cursor[last]
  else cursor[last] = value
  return next
}

/** 结构化编辑器不认识的键（需要原样保留并在 UI 明示）。 */
export function unknownKeys(source: JsonObject, known: string[]): string[] {
  return Object.keys(source)
    .filter((key) => !known.includes(key))
    .sort()
}

export type JsonParseResult = { ok: true; value: JsonObject } | { ok: false; error: string }

export function parseJsonObject(text: string, label = 'JSON'): JsonParseResult {
  const trimmed = text.trim()
  if (!trimmed) return { ok: false, error: `${label} 不能为空；如需清空对象请填写 {}` }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed) as unknown
  } catch (caught) {
    const message = caught instanceof Error ? caught.message : String(caught)
    return { ok: false, error: `${label} 解析失败：${message}` }
  }
  if (!isRecord(parsed)) return { ok: false, error: `${label} 必须是 JSON 对象（{...}）` }
  return { ok: true, value: parsed }
}

export function stringifyJson(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2) ?? '{}'
  } catch {
    return '{}'
  }
}

/** 深比较，用于“表单是否有未保存改动”。 */
export function jsonEqual(a: unknown, b: unknown): boolean {
  try {
    return JSON.stringify(a) === JSON.stringify(b)
  } catch {
    return false
  }
}