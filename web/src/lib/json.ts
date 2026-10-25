/** 原生 JSON 结构的安全编辑工具：路径写入不丢未知字段。 */

export type JsonObject = Record<string, unknown>

export function isRecord(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/**
 * 深拷贝一个纯 JSON 值。
 *
 * 这里**不能**用 `structuredClone`：调用方经常传入 Vue 的响应式代理
 * （例如 NodeEditorView 的 `const draft = reactive<NodeDraft>(...)`），
 * 而 Proxy 不可结构化克隆，`structuredClone` 会抛
 * `DataCloneError: #<Object> could not be cloned.`。这个异常发生在组件的
 * setup 阶段，表现为整页空白（新建节点页曾因此完全不可用）。
 *
 * 本模块的类型就是 `JsonObject`，契约是纯 JSON，不需要 `structuredClone`
 * 才支持的 Map/Set/Date/循环引用，所以 JSON 往返既是正确语义，也没有
 * 可观察的行为差异。
 */
export function cloneJson<T>(value: T): T {
  // 基础类型与 null 原样返回：JSON 往返会把 undefined 丢成 undefined，
  // 但 JSON.parse(undefined) 会抛错，必须提前拦下。
  if (value === null || typeof value !== 'object') return value
  const serialized = JSON.stringify(value)
  if (serialized === undefined) return value
  return JSON.parse(serialized) as T
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

/**
 * 解析 JSON 数组。`externalProxy` 这类入站字段本身就是数组，用 parseJsonObject
 * 会把合法的 `[{...}]` 判成错误。
 */
export function parseJsonArray(text: string, label = 'JSON'): { ok: true; value: unknown[] } | { ok: false; error: string } {
  const trimmed = text.trim()
  if (!trimmed) return { ok: false, error: `${label} 不能为空；如需清空数组请填写 []` }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed) as unknown
  } catch (caught) {
    const message = caught instanceof Error ? caught.message : String(caught)
    return { ok: false, error: `${label} 解析失败：${message}` }
  }
  if (!Array.isArray(parsed)) return { ok: false, error: `${label} 必须是 JSON 数组（[...]）` }
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