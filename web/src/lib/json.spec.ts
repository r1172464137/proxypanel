import { describe, expect, it } from 'vitest'
import { getPath, jsonEqual, parseJsonArray, parseJsonObject, setPath, stringifyJson, unknownKeys } from './json'

describe('json helpers', () => {
  it('setPath 只改目标路径，其它键（含未知键）原样保留', () => {
    const source = {
      network: 'tcp',
      security: 'reality',
      sockopt: { tcpFastOpen: true, mark: 255 },
      realitySettings: { dest: 'example.com:443', settings: { publicKey: 'pub', spiderX: '/' } },
    }
    const next = setPath(source, 'realitySettings.dest', 'other.com:443')
    expect(next.realitySettings).toEqual({ dest: 'other.com:443', settings: { publicKey: 'pub', spiderX: '/' } })
    expect(next.sockopt).toEqual({ tcpFastOpen: true, mark: 255 })
    expect(source.realitySettings.dest).toBe('example.com:443')
  })

  it('setPath 传 undefined 删除键，且不触碰同级字段', () => {
    const next = setPath({ a: { b: 1, c: 2 } }, 'a.b', undefined)
    expect(next).toEqual({ a: { c: 2 } })
  })

  it('getPath 对缺失/非对象安全返回 undefined', () => {
    expect(getPath({ a: {} }, 'a.b.c')).toBeUndefined()
    expect(getPath(null, 'a')).toBeUndefined()
    expect(getPath({ a: 1 }, 'a')).toBe(1)
  })

  it('parseJsonObject 拒绝非对象与非法 JSON，并给出中文错误', () => {
    const array = parseJsonObject('[1,2]', 'stream_settings')
    expect(array.ok).toBe(false)
    if (!array.ok) expect(array.error).toContain('必须是 JSON 对象')

    const invalid = parseJsonObject('{ network: tcp }', 'stream_settings')
    expect(invalid.ok).toBe(false)
    if (!invalid.ok) expect(invalid.error).toContain('解析失败')

    const ok = parseJsonObject('{"network":"ws"}', 'stream_settings')
    expect(ok.ok).toBe(true)
    if (ok.ok) expect(ok.value).toEqual({ network: 'ws' })
  })

  it('unknownKeys 列出结构化编辑器不认识的键', () => {
    expect(unknownKeys({ network: 'tcp', sockopt: {}, mux: {} }, ['network'])).toEqual(['mux', 'sockopt'])
  })

  it('stringifyJson 稳定输出，jsonEqual 深比较', () => {
    expect(stringifyJson({ a: 1 })).toBe('{\n  "a": 1\n}')
    expect(jsonEqual({ a: [1, 2] }, { a: [1, 2] })).toBe(true)
    expect(jsonEqual({ a: 1 }, { a: 2 })).toBe(false)
  })
})
describe('parseJsonArray', () => {
  it('接受数组并拒绝对象', () => {
    const ok = parseJsonArray('[{"dest":"a.example","port":443}]', 'external_proxy')
    expect(ok.ok).toBe(true)
    if (ok.ok) expect(ok.value).toHaveLength(1)

    const bad = parseJsonArray('{"dest":"a.example"}', 'external_proxy')
    expect(bad.ok).toBe(false)
    if (!bad.ok) expect(bad.error).toContain('数组')

    const malformed = parseJsonArray('[oops', 'external_proxy')
    expect(malformed.ok).toBe(false)

    const empty = parseJsonArray('   ', 'external_proxy')
    expect(empty.ok).toBe(false)
  })
})
