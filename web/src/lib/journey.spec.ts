import { describe, expect, it, vi } from 'vitest'
import { allPages } from './allPages'
import { usePagedList } from './paged'
import { applyStreamView, streamView } from './nodeDraft'
import { submitRequest } from './batch'
import type { BatchPreview } from '@/api/types'

describe('服务交付数据守恒', () => {
  it('选择器逐页获取真实范围，不把首个200条当全部', async () => {
    const load = vi.fn(async (cursor: string | null) => cursor ? { items: ['n201'], next_cursor: null } : { items: ['n1'], next_cursor: 'opaque' })
    expect(await allPages(load)).toEqual(['n1', 'n201'])
    expect(load.mock.calls).toEqual([[null], ['opaque']])
  })
  it('游标重复或分页失败阻止将不完整范围作为完整列表', async () => {
    await expect(allPages(async () => ({ items: ['n1'], next_cursor: 'loop' }))).rejects.toThrow('游标重复')
    const failure = new Error('读取失败')
    await expect(allPages(async () => { throw failure })).rejects.toBe(failure)
  })
  it('晚到旧搜索不能覆盖新搜索或错误态', async () => {
    let oldResolve!: (page: { items: string[]; next_cursor: null }) => void
    let calls = 0
    const list = usePagedList<string>(() => ++calls === 1 ? new Promise(resolve => { oldResolve = resolve }) : Promise.resolve({ items: ['new'], next_cursor: null }))
    const first = list.refresh()
    await list.refresh()
    oldResolve({ items: ['old'], next_cursor: null })
    await first
    expect(list.items.value).toEqual(['new'])
    expect(list.loading.value).toBe(false)
  })
  it('切页失败不伪造页码，不留下旧结果冒充新页', async () => {
    const list = usePagedList(vi.fn().mockResolvedValueOnce({ items: ['first'], next_cursor: 'next' }).mockRejectedValueOnce(new Error('故障')))
    await list.refresh()
    await list.next()
    expect(list.page.value).toBe(1)
    expect(list.items.value).toEqual([])
    expect(list.error.value?.message).toBe('故障')
  })
  it('结构化编辑保留多证书与未知高级字段', () => {
    const source = { network: 'tcp', security: 'tls', futureOption: { keep: true }, tlsSettings: { certificates: [ { certificateFile: '/a.pem', keyFile: '/a.key', usage: 'encipherment', ocspStapling: 99 }, { certificate: ['second'], key: ['secret'], future: true } ], extra: 'keep' } }
    const edited = applyStreamView(source, streamView(source))
    expect(edited).toEqual(source)
    expect(source.tlsSettings.certificates).toHaveLength(2)
  })
  it('提交携带服务端预览的规则版本和成员摘要', () => {
    expect(submitRequest({ id: 'p', rules_version: 'rv', member_set_digest: 'digest' } as unknown as BatchPreview)).toEqual({ preview_id: 'p', rules_version: 'rv', member_set_digest: 'digest', confirm: true })
  })
})
