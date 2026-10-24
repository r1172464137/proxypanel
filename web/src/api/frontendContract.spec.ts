import { afterEach, describe, expect, it, vi } from 'vitest'
import { adminApi, batchDetailFromEnvelope, meApi } from './endpoints'
import { ApiError } from './http'

function reply(payload: unknown) {
  return new Response(JSON.stringify(payload), { status: 200, headers: { 'Content-Type': 'application/json' } })
}
afterEach(() => vi.unstubAllGlobals())

describe('frontend contract boundaries', () => {
  it('unwraps the real batch envelope without losing persistent detail or result identity', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply({ data: {
      batch: { id: 'bat_test', state: 'partial', operation: 'set_active', created_at: '2026-01-01T00:00:00Z' },
      results: [{ member_id: 'm_test', state: 'failed', task_id: 't_test' }],
      summary: { failed: 1 },
    } })))
    const result = await adminApi.batches.get('bat_test')
    expect(result).toMatchObject({ id: 'bat_test', state: 'partial', operation: 'set_active', created_at: '2026-01-01T00:00:00Z', summary: { failed: 1 } })
    expect(result.results).toEqual([{ member_id: 'm_test', state: 'failed', task_id: 't_test' }])
  })

  it.each([null, {}, { id: 'b' }, { batch: { id: 'b' }, results: [null] }])('rejects malformed batch detail rather than rendering an empty success (%j)', payload => {
    expect(() => batchDetailFromEnvelope(payload)).toThrow(ApiError)
  })

  it('supports a flat complete batch without manufacturing a state', () => {
    const result = batchDetailFromEnvelope({ id: 'bat_flat', results: [] })
    expect(result.id).toBe('bat_flat')
    expect(result.state).toBeUndefined()
    expect(result.results).toEqual([])
  })

  it('sends only supported event/task scopes and retains literal search characters', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => { calls.push(url); return reply({ items: [], next_cursor: null }) }))
    await adminApi.events.list({ state: 'open', q: 'literal%_', limit: 30 })
    await adminApi.tasks.list({ state: 'failed', agent_id: 'a_test', batch_id: 'b_test', cursor: 'opaque' })
    const events = new URL(calls[0]!, 'http://fixture.invalid').searchParams
    expect(Object.fromEntries(events)).toEqual({ limit: '30', state: 'open', q: 'literal%_' })
    const tasks = new URL(calls[1]!, 'http://fixture.invalid').searchParams
    expect(Object.fromEntries(tasks)).toEqual({ cursor: 'opaque', state: 'failed', agent_id: 'a_test', batch_id: 'b_test' })
  })

  it('continues member node and session pages instead of silently repeating page one', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => { calls.push(url); return reply({ items: [], next_cursor: null }) }))
    await meApi.nodes({ cursor: 'nodes-next', limit: 50 })
    await meApi.sessions({ cursor: 'sessions-next', limit: 20 })
    expect(new URL(calls[0]!, 'http://fixture.invalid').searchParams.get('cursor')).toBe('nodes-next')
    expect(new URL(calls[1]!, 'http://fixture.invalid').searchParams.get('cursor')).toBe('sessions-next')
  })

  it('does not invent NIC telemetry in the usage contract', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply({ items: [], next_cursor: 'usage-next' })))
    const result = await adminApi.usage.query({ limit: 1 })
    expect(result).toEqual({ items: [], next_cursor: 'usage-next' })
    expect(result).not.toHaveProperty('nic_counters')
  })
})
