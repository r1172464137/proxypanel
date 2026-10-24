import { describe, expect, it } from 'vitest'
import { unpackMeUsage } from './meUsage'
describe('成员用量对象解包', () => {
  it('空 usage 不丢调整，保留游标', () => {
    const adjustments = [{ id: 'adjustment-a', bytes: 1024 }]
    expect(unpackMeUsage({ items: { usage: [], adjustments }, next_cursor: 'opaque' })).toEqual({ items: [], adjustments, next_cursor: 'opaque' })
  })
  it('明细与调整分别解包，历史数组不崩溃', () => {
    const rows = [{ up_bytes: 0, down_bytes: 100 }]
    expect(unpackMeUsage({ items: { usage: rows, adjustments: [] } }).items).toEqual(rows)
    expect(unpackMeUsage({ items: rows })).toEqual({ items: rows, adjustments: [], next_cursor: null })
  })
  it('畸形数据不假装空账本', () => {
    expect(() => unpackMeUsage({ items: {} })).toThrow('无法读取')
    expect(() => unpackMeUsage({ items: { usage: [null], adjustments: [] } })).toThrow('不完整')
  })
})
