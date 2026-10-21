import { describe, expect, it } from 'vitest'
import {
  bytesToGib,
  formatBytes,
  formatDateTime,
  formatQuota,
  fromLocalInput,
  gibToBytes,
  isValidTimezone,
  toLocalInput,
} from './format'

describe('format', () => {
  it('字节按 1024 基数并标注单位', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1536)).toBe('1.50 KiB')
    expect(formatBytes(5368709120)).toBe('5.00 GiB')
    expect(formatBytes(null)).toBe('—')
    expect(formatBytes(-1073741824)).toBe('-1.00 GiB')
  })

  it('GiB ↔ 字节往返', () => {
    expect(gibToBytes('1')).toBe(1073741824)
    expect(gibToBytes('1.5')).toBe(1610612736)
    expect(gibToBytes('abc')).toBeNull()
    expect(gibToBytes(-1)).toBeNull()
    expect(bytesToGib(10737418240)).toBe('10')
  })

  it('不限量额度显示', () => {
    expect(formatQuota(null)).toBe('不限量')
    expect(formatQuota(undefined)).toBe('未提供')
    expect(formatQuota(1073741824)).toBe('1.00 GiB')
  })

  it('时间按业务时区显示并标注时区', () => {
    expect(formatDateTime('2026-11-01T00:00:00Z', 'Asia/Shanghai')).toBe('2026-11-01 08:00 (Asia/Shanghai)')
    expect(formatDateTime(null)).toBe('—')
  })

  it('无效时区不猜测，退回原始字符串', () => {
    const iso = '2026-11-01T00:00:00Z'
    expect(formatDateTime(iso, 'Asia/NotAZone')).toBe(iso)
    expect(isValidTimezone('Asia/Shanghai')).toBe(true)
    expect(isValidTimezone('Not/AZone')).toBe(false)
    expect(isValidTimezone('')).toBe(false)
  })

  it('datetime-local 往返保留同一瞬间', () => {
    const iso = '2026-11-30T04:30:00Z'
    const local = toLocalInput(iso)
    expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
    expect(fromLocalInput(local)).toBe(iso)
    expect(fromLocalInput('')).toBeNull()
    expect(fromLocalInput('not-a-date')).toBeNull()
  })
})