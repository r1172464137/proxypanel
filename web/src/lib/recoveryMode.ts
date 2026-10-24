import { onMounted, onBeforeUnmount, watch } from 'vue'
import { adminApi } from '@/api/endpoints'
import { sessionState } from '@/stores/session'
import { useResource, usePolling } from './async'

// The only cross-page server resource. Identity epochs reject late responses after logout.
let epoch = 0
let consumers = 0
const identity = () => sessionState.user?.role === 'admin' ? sessionState.user.id ?? sessionState.user.username : null
const resource = useResource(async () => {
  const started = epoch
  if (!identity()) throw new DOMException('No administrator session', 'AbortError')
  const result = await adminApi.recoveryMode.get()
  if (started !== epoch) throw new DOMException('Session changed', 'AbortError')
  return result
})
let flight: Promise<void> | null = null
function reload(): Promise<void> {
  if (!identity()) return Promise.resolve()
  if (!flight) {
    const current = resource.reload().finally(() => { if (flight === current) flight = null })
    flight = current
  }
  return flight
}
const poll = usePolling(() => { if (!document.hidden) return reload() }, 30_000)
export function clearRecoveryMode(): void {
  epoch++
  flight = null
  resource.reset()
}
watch(identity, () => {
  clearRecoveryMode()
  poll.stop()
  if (identity() && consumers) { void reload(); poll.start() }
}, { flush: 'sync' })

export function useRecoveryMode() {
  onMounted(() => {
    consumers++
    if (identity()) {
      if (!resource.loaded.value) void reload()
      poll.start()
    }
  })
  onBeforeUnmount(() => { if (--consumers === 0) poll.stop() })
  return { ...resource, reload }
}

// A successful mutation invalidates every reader, including the persistent banner.
export async function mutateRecoveryMode(action: () => Promise<unknown>): Promise<unknown> {
  if (!identity()) throw new Error('此操作需要管理员权限')
  const started = epoch
  const result = await action()
  if (started === epoch) { clearRecoveryMode(); await reload() }
  return result
}
