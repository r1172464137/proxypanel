/** 加载/提交状态统一封装：显式 loading / error / retry，绝不静默吞错。 */
import { ref, shallowRef, type Ref, type ShallowRef } from 'vue'
import { toApiError } from './errors'
import type { ApiError } from '@/api/http'

export interface Resource<T> {
  data: ShallowRef<T | null>
  error: ShallowRef<ApiError | null>
  loading: Ref<boolean>
  loaded: Ref<boolean>
  reload: () => Promise<void>
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

  return { data, error, loading, loaded, reload }
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

/** 轮询任务/状态的简单定时器封装（组件卸载时停止）。 */
export function usePolling(callback: () => void | Promise<void>, intervalMs: number, immediate = false) {
  let timer: number | null = null
  let stopped = false

  async function tick(): Promise<void> {
    if (stopped) return
    await callback()
    if (stopped) return
    timer = window.setTimeout(() => void tick(), intervalMs)
  }

  function start(): void {
    stopped = false
    if (timer !== null) return
    if (immediate) void tick()
    else timer = window.setTimeout(() => void tick(), intervalMs)
  }

  function stop(): void {
    stopped = true
    if (timer !== null) {
      window.clearTimeout(timer)
      timer = null
    }
  }

  return { start, stop }
}