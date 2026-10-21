/** 展示层格式化：字节、时间与时区、相对时间。全部为纯函数，便于测试。 */

const BASE = 1024
const UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

export function formatNumber(value: number): string {
  return new Intl.NumberFormat('zh-CN').format(value)
}

/** 字节 → 人类可读，显式标注二进制基数（1024）。 */
export function formatBytes(bytes: number | null | undefined, digits = 2): string {
  if (bytes === null || bytes === undefined || Number.isNaN(bytes)) return '—'
  if (!Number.isFinite(bytes)) return '—'
  const sign = bytes < 0 ? '-' : ''
  let value = Math.abs(bytes)
  let unit = 0
  while (value >= BASE && unit < UNITS.length - 1) {
    value /= BASE
    unit += 1
  }
  const fixed = unit === 0 ? String(Math.round(value)) : value.toFixed(digits)
  return `${sign}${fixed} ${UNITS[unit]}`
}

export function bytesToGib(bytes: number | null | undefined, digits = 2): string {
  if (bytes === null || bytes === undefined || Number.isNaN(bytes)) return ''
  const gib = bytes / BASE ** 3
  return gib.toFixed(digits).replace(/\.?0+$/, '')
}

/** GiB 文本 → 字节；非法输入返回 null。 */
export function gibToBytes(text: string | number): number | null {
  const value = typeof text === 'number' ? text : Number(text.trim())
  if (!Number.isFinite(value) || value < 0) return null
  return Math.round(value * BASE ** 3)
}

/** 额度展示：null=不限量（服务端显式），undefined=接口未提供（不推断为不限量）。 */
export function formatQuota(bytes: number | null | undefined): string {
  if (bytes === null) return '不限量'
  if (bytes === undefined) return '未提供'
  return formatBytes(bytes)
}

export function isValidTimezone(tz: string): boolean {
  if (!tz || !tz.trim()) return false
  try {
    new Intl.DateTimeFormat('zh-CN', { timeZone: tz })
    return true
  } catch {
    return false
  }
}

function parts(iso: string, timeZone?: string | null): Intl.DateTimeFormatPart[] | null {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return null
  const options: Intl.DateTimeFormatOptions = {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    timeZone: timeZone || undefined,
  }
  try {
    return new Intl.DateTimeFormat('zh-CN', options).formatToParts(date)
  } catch {
    return null
  }
}

function pick(list: Intl.DateTimeFormatPart[], type: Intl.DateTimeFormatPartTypes): string {
  return list.find((part) => part.type === type)?.value ?? ''
}

/**
 * RFC3339 UTC → 业务时区展示，例如 "2026-11-01 00:00 (Asia/Shanghai)"。
 * 时区未提供时按浏览器本地时区显示（不声称是业务时区）。
 */
export function formatDateTime(iso: string | null | undefined, timeZone?: string | null): string {
  if (!iso) return '—'
  const list = parts(iso, timeZone)
  if (!list) return iso
  const text = `${pick(list, 'year')}-${pick(list, 'month')}-${pick(list, 'day')} ${pick(list, 'hour')}:${pick(list, 'minute')}`
  return timeZone ? `${text} (${timeZone})` : text
}

export function formatDate(iso: string | null | undefined, timeZone?: string | null): string {
  if (!iso) return '—'
  const list = parts(iso, timeZone)
  if (!list) return iso
  return `${pick(list, 'year')}-${pick(list, 'month')}-${pick(list, 'day')}`
}

/** 悬停提示用：保留原始 UTC 时间，不做时区猜测。 */
export function utcLabel(iso: string | null | undefined): string {
  if (!iso) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return `${date.toISOString()} (UTC)`
}

export function formatRelative(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return '—'
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  const diffSeconds = Math.round((now - date.getTime()) / 1000)
  const future = diffSeconds < 0
  const abs = Math.abs(diffSeconds)
  let text: string
  if (abs < 45) return future ? '即将' : '刚刚'
  if (abs < 3600) text = `${Math.round(abs / 60)} 分钟`
  else if (abs < 86400) text = `${Math.round(abs / 3600)} 小时`
  else text = `${Math.round(abs / 86400)} 天`
  return future ? `${text}后` : `${text}前`
}

/** datetime-local 输入（浏览器本地时间）↔ RFC3339 UTC。 */
export function toLocalInput(iso: string | null | undefined): string {
  if (!iso) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function fromLocalInput(value: string | null | undefined): string | null {
  if (!value) return null
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return null
  // 秒级精度即可，去掉毫秒后缀，便于服务端 RFC3339 校验
  return date.toISOString().replace(/\.000Z$/, 'Z')
}

export function shortId(id: string | null | undefined, keep = 8): string {
  if (!id) return '—'
  return id.length <= keep ? id : `${id.slice(0, keep)}…`
}

/** 安全 JSON 展示（用于审计/事件详情，禁止 v-html）。 */
export function prettyJson(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2) ?? ''
  } catch {
    return String(value)
  }
}