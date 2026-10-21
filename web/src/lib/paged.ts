/** 游标分页列表（limit + next_cursor，不猜 offset）。 */
import { ref, shallowRef, type Ref, type ShallowRef } from 'vue'
import type { PageResult } from '@/api/http'
import { toApiError } from './errors'
import type { ApiError } from '@/api/http'

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

export function usePagedList<T>(loader: (cursor: string | null) => Promise<PageResult<T>>, limitHint?: number): PagedList<T> {
  const items = shallowRef<T[]>([])
  const error = shallowRef<ApiError | null>(null)
  const loading = ref(false)
  const nextCursor = ref<string | null>(null)
  const page = ref(1)
  const history: (string | null)[] = [null]

  async function load(cursor: string | null): Promise<void> {
    loading.value = true
    error.value = null
    try {
      const result = await loader(cursor)
      items.value = result.items
      nextCursor.value = result.next_cursor
      if (limitHint && result.items.length > limitHint) items.value = result.items.slice(0, limitHint)
    } catch (caught) {
      error.value = toApiError(caught)
      items.value = []
      nextCursor.value = null
    } finally {
      loading.value = false
    }
  }

  async function refresh(): Promise<void> {
    history.length = 0
    history.push(null)
    page.value = 1
    await load(null)
  }

  async function next(): Promise<void> {
    if (!nextCursor.value) return
    const cursor = nextCursor.value
    history.push(cursor)
    page.value += 1
    await load(cursor)
  }

  async function prev(): Promise<void> {
    if (history.length <= 1) return
    history.pop()
    const cursor = history[history.length - 1] ?? null
    page.value = Math.max(1, page.value - 1)
    await load(cursor)
  }

  return { items, error, loading, nextCursor, page, refresh, next, prev }
}