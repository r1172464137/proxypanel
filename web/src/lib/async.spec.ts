import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { usePolling, useResource } from './async'

const flush = async () => { await Promise.resolve(); await Promise.resolve() }

describe('polling visibility and ownership', () => {
  let hidden = false
  beforeEach(() => {
    vi.useFakeTimers()
    hidden = false
    vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
  })
  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })
  function visibility(value: boolean) {
    hidden = value
    document.dispatchEvent(new Event('visibilitychange'))
  }

  it('does not poll while hidden; visibility resumes one fresh read and stop is final', async () => {
    const callback = vi.fn()
    const poller = usePolling(callback, 1000, true)
    visibility(true)
    poller.start()
    await vi.advanceTimersByTimeAsync(3000)
    expect(callback).not.toHaveBeenCalled()
    visibility(false)
    await flush()
    expect(callback).toHaveBeenCalledTimes(1)
    visibility(true)
    await vi.advanceTimersByTimeAsync(5000)
    expect(callback).toHaveBeenCalledTimes(1)
    poller.stop()
    visibility(false)
    await vi.advanceTimersByTimeAsync(3000)
    expect(callback).toHaveBeenCalledTimes(1)
  })

  it('never overlaps a pending read across hide/show and explicit restart', async () => {
    let release: (() => void) | undefined
    const pending = new Promise<void>(resolve => { release = resolve })
    const callback = vi.fn(() => pending)
    const poller = usePolling(callback, 1000, true)
    poller.start()
    visibility(true)
    visibility(false)
    poller.stop()
    poller.start()
    expect(callback).toHaveBeenCalledTimes(1)
    release!()
    await flush()
    await vi.advanceTimersByTimeAsync(1000)
    expect(callback).toHaveBeenCalledTimes(2)
    poller.stop()
  })

  it('exposes callback failure and stops until the owner explicitly retries', async () => {
    const callback = vi.fn().mockRejectedValueOnce(new Error('synthetic-poll-failure')).mockResolvedValue(undefined)
    const poller = usePolling(callback, 1000, true)
    poller.start()
    await flush()
    expect(poller.error.value?.message).toBe('synthetic-poll-failure')
    await vi.advanceTimersByTimeAsync(5000)
    expect(callback).toHaveBeenCalledTimes(1)
    poller.start()
    await flush()
    expect(poller.error.value).toBeNull()
    expect(callback).toHaveBeenCalledTimes(2)
    poller.stop()
  })

  it('releases the visibility subscription when its Vue scope is disposed', async () => {
    const callback = vi.fn()
    const scope = effectScope()
    scope.run(() => usePolling(callback, 1000, true).start())
    await flush()
    expect(callback).toHaveBeenCalledTimes(1)
    scope.stop()
    visibility(true)
    visibility(false)
    await vi.advanceTimersByTimeAsync(3000)
    expect(callback).toHaveBeenCalledTimes(1)
  })
})

describe('resource identity invalidation', () => {
  it('reset cancels an old identity and an ignored late reply cannot restore it', async () => {
    let release: ((value: string) => void) | undefined
    let signal: AbortSignal | undefined
    const resource = useResource<string>(s => {
      signal = s
      return new Promise(resolve => { release = resolve })
    })
    const old = resource.reload()
    resource.reset()
    expect(signal?.aborted).toBe(true)
    release!('old-account-only-data')
    await old
    expect(resource.data.value).toBeNull()
    expect(resource.loaded.value).toBe(false)
    expect(resource.loading.value).toBe(false)
    expect(resource.error.value).toBeNull()
  })
})
