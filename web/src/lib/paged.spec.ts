import { describe, expect, it, vi } from 'vitest'
import { ApiError, type PageResult } from '@/api/http'
import { usePagedList } from './paged'

describe('cursor scope boundaries', () => {
  it('invalidate synchronously retires in-flight selection sources without fetching', async () => {
    let resolve!: (page: PageResult<string>) => void
    const loader = vi.fn(() => new Promise<PageResult<string>>(done => { resolve = done }))
    const list = usePagedList(loader)
    const old = list.refresh()
    expect(list.invalidate()).toBe(true)
    resolve({ items: ['old'], next_cursor: 'old-cursor' })
    await old
    expect(list.items.value).toEqual([])
    expect(list.page.value).toBe(1)
    expect(list.loading.value).toBe(false)
    expect(loader).toHaveBeenCalledTimes(1)
  })
  it('expired cursors reset history visibly and do not retry in a loop', async () => {
    const loader = vi.fn().mockResolvedValueOnce({ items: ['one'], next_cursor: 'c2' })
      .mockRejectedValueOnce(new ApiError(400, { code: 'CURSOR_INVALID', message: 'expired' }))
      .mockResolvedValueOnce({ items: ['fresh'], next_cursor: null })
    const list = usePagedList<string>(loader)
    await list.refresh()
    await list.next()
    expect(list.page.value).toBe(1)
    expect(list.error.value?.message).toContain('列表已过期')
    expect(list.items.value).toEqual([])
    expect(loader).toHaveBeenCalledTimes(2)
    await list.refresh()
    expect(loader).toHaveBeenLastCalledWith(null)
    expect(list.items.value).toEqual(['fresh'])
  })
})
