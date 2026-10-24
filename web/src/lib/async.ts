/** 加载/提交状态统一封装：显式 loading / error / retry，绝不静默吞错。 */
import { getCurrentScope, onScopeDispose, ref, shallowRef, type Ref, type ShallowRef } from 'vue'
import { toApiError } from './errors'
import type { ApiError } from '@/api/http'

export interface Resource<T> {
  data: ShallowRef<T | null>
  error: ShallowRef<ApiError | null>
  loading: Ref<boolean>
  loaded: Ref<boolean>
  reload: () => Promise<void>
  reset: () => void
}

export function useResource<T>(loader: (signal: AbortSignal) => Promise<T>): Resource<T> {
  const data = shallowRef<T | null>(null)
  const error = shallowRef<ApiError | null>(null)
  const loading = ref(false)
  const loaded = ref(false)
  let controller: AbortController | null = null

  async function reload(): Promise<void> {
    controller?.abort()
    controller = new AbortController()
    const current = controller
    loading.value = true
    error.value = null
    try {
      const result = await loader(current.signal)
      if (current !== controller) return
      data.value = result
      loaded.value = true
    } catch (caught) {
      if (current !== controller) return
      if (caught instanceof DOMException && caught.name === 'AbortError') return
      error.value = toApiError(caught)
      data.value = null
    } finally {
      if (current === controller) loading.value = false
    }
  }

  function reset(): void {
    controller?.abort()
    controller = null
    data.value = null
    error.value = null
    loading.value = false
    loaded.value = false
  }

  return { data, error, loading, loaded, reload, reset }
}

export interface Action<Args extends unknown[], R> {
  pending: Ref<boolean>
  error: ShallowRef<ApiError | null>
  data: ShallowRef<R | null>
  run: (...args: Args) => Promise<R | undefined>
  reset: () => void
}

export function useAction<Args extends unknown[], R>(fn: (...args: Args) => Promise<R>): Action<Args, R> {
  const pending = ref(false)
  const error = shallowRef<ApiError | null>(null)
  const data = shallowRef<R | null>(null)

  async function run(...args: Args): Promise<R | undefined> {
    pending.value = true
    error.value = null
    try {
      const result = await fn(...args)
      data.value = result
      return result
    } catch (caught) {
      if (caught instanceof DOMException && caught.name === 'AbortError') return undefined
      error.value = toApiError(caught)
      return undefined
    } finally {
      pending.value = false
    }
  }

  function reset(): void {
    error.value = null
    data.value = null
  }

  return { pending, error, data, run, reset }
}

/** Poll only while visible, with one in-flight read and explicit lifetime ownership. */
export function usePolling(callback: () => void | Promise<void>, intervalMs: number, immediate = false) {
  const error = shallowRef<ApiError | null>(null)
  let timer: number | null = null
  let active = false
  let inFlight = false
  let generation = 0

  function clearTimer(): void {
    if (timer !== null) window.clearTimeout(timer)
    timer = null
  }

  function schedule(): void {
    if (!active || document.hidden || inFlight || timer !== null) return
    const expected = generation
    timer = window.setTimeout(() => void tick(expected), intervalMs)
  }

  async function tick(expected: number): Promise<void> {
    if (expected !== generation) return
    timer = null
    if (!active || document.hidden || inFlight) return
    inFlight = true
    try {
      await callback()
    } catch (caught) {
      if (expected === generation && active) {
        error.value = toApiError(caught)
        stop() // Unexpected callback failures need an explicit retry, not a hidden loop.
      }
    } finally {
      inFlight = false
      schedule()
    }
  }

  function visibilityChanged(): void {
    clearTimer()
    if (!document.hidden && active && !inFlight) void tick(generation)
  }

  function start(): void {
    if (active) return
    active = true
    generation++
    error.value = null
    document.addEventListener('visibilitychange', visibilityChanged)
    if (immediate && !document.hidden) void tick(generation)
    else schedule()
  }

  function stop(): void {
    active = false
    generation++
    clearTimer()
    document.removeEventListener('visibilitychange', visibilityChanged)
  }

  if (getCurrentScope()) onScopeDispose(stop)
  return { start, stop, error }
}