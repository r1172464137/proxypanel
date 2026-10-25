import { describe, expect, it } from 'vitest'
import { STREAM_FIELDS, fieldsFor, SECURITY_OPTIONS, NETWORK_ALIASES } from './streamSchema'
import { applyStreamView, streamView } from './nodeDraft'
import { getPath } from './json'

// 字段表是照抄 3x-ui 入站表单的唯一来源，所以它本身要被钉住：唯一 key、唯一写入
// 路径、每个字段都能往返。加字段时忘记接管序列化会在这里直接失败。
describe('streamSchema 字段表', () => {
  it('key 唯一，写入路径唯一', () => {
    const keys = STREAM_FIELDS.map((f) => f.key)
    expect(new Set(keys).size).toBe(keys.length)
    const written = STREAM_FIELDS.map((f) => f.paths[0])
    expect(new Set(written).size).toBe(written.length)
  })

  it('每个字段都有标签与至少一个读取路径', () => {
    for (const field of STREAM_FIELDS) {
      expect(field.label.length, field.key).toBeGreaterThan(0)
      expect(field.paths.length, field.key).toBeGreaterThan(0)
    }
  })

  it('select 字段必须声明 options', () => {
    for (const field of STREAM_FIELDS.filter((f) => f.kind === 'select')) {
      expect(field.options?.length, field.key).toBeGreaterThan(1)
    }
  })

  // 逐字段往返：写进去的值必须能按同一字段读回来，且不产生额外的同义键。
  it('每个字段都能写入并读回', () => {
    for (const field of STREAM_FIELDS) {
      // 证书文件存在 certificates[] 数组里，下面单独验证。
      if (field.key === 'tlsCertFile' || field.key === 'tlsKeyFile') continue
      const network = field.networks?.[0] ?? 'tcp'
      const security = field.securities?.[0] ?? 'none'
      const base = { network, security }
      const view = streamView(base)
      const sample = field.kind === 'bool' ? true : field.kind === 'number' ? '123' : 'sample-value'
      const saved = applyStreamView(base, { ...view, [field.key]: sample })
      const key = field.paths[0]
      const written = getPath(saved, key)
      expect(written, `${field.key} -> ${key}`).toBeDefined()
      const reread = streamView(saved)[field.key]
      if (field.kind === 'bool') expect(reread, field.key).toBe(true)
      else if (field.kind === 'number') expect(reread, field.key).toBe('123')
      else expect(reread, field.key).toBe('sample-value')
    }
  })

  it('证书文件写进 certificates[] 并能读回', () => {
    const base = { network: 'tcp', security: 'tls' }
    const view = streamView(base)
    const saved = applyStreamView(base, {
      ...view, tlsCertFile: '/etc/xray/fullchain.pem', tlsKeyFile: '/etc/xray/privkey.pem',
    })
    const certs = getPath(saved, 'tlsSettings.certificates') as Record<string, unknown>[]
    expect(certs?.[0]?.certificateFile).toBe('/etc/xray/fullchain.pem')
    expect(certs?.[0]?.keyFile).toBe('/etc/xray/privkey.pem')
    expect(streamView(saved).tlsCertFile).toBe('/etc/xray/fullchain.pem')
    expect(streamView(saved).tlsKeyFile).toBe('/etc/xray/privkey.pem')
  })

  it('按传输层与加密层过滤字段', () => {
    const tcp = fieldsFor('transport', 'tcp', 'none').map((f) => f.key)
    expect(tcp).toContain('acceptProxyProtocol')
    expect(tcp).not.toContain('wsPath')

    const ws = fieldsFor('transport', 'ws', 'none').map((f) => f.key)
    expect(ws).toContain('wsPath')
    expect(ws).not.toContain('acceptProxyProtocol')

    // mkcp 是 kcp 的旧别名，不能因为别名就丢掉 kcp 的字段。
    expect(fieldsFor('transport', 'mkcp', 'none').map((f) => f.key)).toContain('kcpMtu')
    expect(NETWORK_ALIASES.mkcp).toBe('kcp')

    expect(fieldsFor('reality', 'tcp', 'tls')).toHaveLength(0)
    expect(fieldsFor('reality', 'tcp', 'reality').map((f) => f.key)).toContain('realityXver')
    expect(fieldsFor('tls', 'tcp', 'reality')).toHaveLength(0)
    expect(SECURITY_OPTIONS).toEqual(['none', 'tls', 'reality'])
  })
})

describe('numeric select 字段', () => {
  it('xver 与 tcpFastOpen 写成数字，不是字符串', () => {
    const base = { network: 'tcp', security: 'reality' }
    const view = streamView(base)
    const saved = applyStreamView(base, { ...view, realityXver: '1', sockTcpFastOpen: '256' })
    expect(getPath(saved, 'realitySettings.xver')).toBe(1)
    expect(getPath(saved, 'sockopt.tcpFastOpen')).toBe(256)
    expect(typeof getPath(saved, 'realitySettings.xver')).toBe('number')
  })
})
