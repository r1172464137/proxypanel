import type { PageResult } from '@/api/http'

/** Reference selectors must follow real cursors; never silently truncate at 200. */
export async function allPages<T>(load: (cursor: string | null) => Promise<PageResult<T>>): Promise<T[]> {
  const items: T[] = []
  const seen = new Set<string>()
  let cursor: string | null = null
  do {
    const page = await load(cursor)
    items.push(...page.items)
    cursor = page.next_cursor
    if (cursor && seen.has(cursor)) throw new Error('列表游标重复，请刷新后重试；尚未获取完整选择范围')
    if (cursor) seen.add(cursor)
  } while (cursor)
  return items
}
