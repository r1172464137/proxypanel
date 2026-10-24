/** Cursor pagination: latest query wins; failure never leaves a stale selection source. */
import { ref, shallowRef, type Ref, type ShallowRef } from 'vue'
import type { PageResult, ApiError } from '@/api/http'
import { toApiError } from './errors'

export interface PagedList<T> {
  items: ShallowRef<T[]>
  error: ShallowRef<ApiError | null>
  loading: Ref<boolean>
  nextCursor: Ref<string | null>
  page: Ref<number>
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
  async function load(target: (string | null)[]): Promise<void> {
    const current = ++request
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
      page.value = target.length
    } catch (caught) {
      if (current !== request) return
      error.value = toApiError(caught)
    } finally {
      if (current === request) loading.value = false
    }
  }
  async function refresh(): Promise<void> { await load([null]) }
  async function next(): Promise<void> {
    if (!loading.value && nextCursor.value) await load([...history, nextCursor.value])
  }
  async function prev(): Promise<void> {
    if (!loading.value && history.length > 1) await load(history.slice(0, -1))
  }
  return { items, error, loading, nextCursor, page, refresh, next, prev }
}
