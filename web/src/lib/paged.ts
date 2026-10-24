/** Cursor pagination: latest query wins; failure never leaves a stale selection source. */
import { ref, shallowRef, type Ref, type ShallowRef } from 'vue'
import { ApiError, type PageResult } from '@/api/http'
import { toApiError } from './errors'

export interface PagedList<T> {
  items: ShallowRef<T[]>
  error: ShallowRef<ApiError | null>
  loading: Ref<boolean>
  nextCursor: Ref<string | null>
  page: Ref<number>
  /** Changes before every navigation/refresh, including revisits to the same cursor. */
  revision: Ref<number>
  visitedCursors: ShallowRef<(string | null)[]>
  invalidate: () => boolean
  refresh: () => Promise<void>
  next: () => Promise<void>
  prev: () => Promise<void>
}
export function usePagedList<T>(loader: (cursor: string | null) => Promise<PageResult<T>>, _limitHint?: number): PagedList<T> {
  const items = shallowRef<T[]>([])
  const error = shallowRef<ApiError | null>(null)
  const loading = ref(false)
  const nextCursor = ref<string | null>(null)
  const page = ref(1)
  let history: (string | null)[] = [null]
  let request = 0
  const revision = ref(0)
  const visitedCursors = shallowRef<(string | null)[]>([])
  function invalidate(): boolean {
    const changed = loading.value || items.value.length > 0 || history.length > 1 || visitedCursors.value.length > 0
    request++
    revision.value++
    history = [null]
    page.value = 1
    items.value = []
    nextCursor.value = null
    visitedCursors.value = []
    loading.value = false
    error.value = null
    return changed
  }
  async function load(target: (string | null)[]): Promise<void> {
    const current = ++request
    revision.value++
    loading.value = true
    error.value = null
    items.value = []
    nextCursor.value = null
    try {
      const result = await loader(target.at(-1) ?? null)
      if (current !== request) return
      items.value = result.items
      nextCursor.value = result.next_cursor
      history = target
      visitedCursors.value = [...new Set([...visitedCursors.value, target.at(-1) ?? null])]
      page.value = target.length
    } catch (caught) {
      if (current !== request) return
      const failure = toApiError(caught)
      if (failure.code === 'CURSOR_INVALID') {
        invalidate()
        error.value = new ApiError(failure.status, { code: failure.code, message: '列表已过期，已清空范围并返回首页。请点击重试读取第一页。', details: failure.details, request_id: failure.requestId })
      } else error.value = failure
    } finally {
      if (current === request) loading.value = false
    }
  }
  async function refresh(): Promise<void> { invalidate(); await load([null]) }
  async function next(): Promise<void> {
    if (!loading.value && nextCursor.value) await load([...history, nextCursor.value])
  }
  async function prev(): Promise<void> {
    if (!loading.value && history.length > 1) await load(history.slice(0, -1))
  }
  return { items, error, loading, nextCursor, page, revision, visitedCursors, invalidate, refresh, next, prev }
}
