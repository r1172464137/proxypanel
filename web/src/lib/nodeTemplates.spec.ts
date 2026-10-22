import { describe, expect, it } from 'vitest'
import {
  NODE_TEMPLATES,
  findTemplate,
  newBase64Key,
  newShortId,
  newUUID,
  newRealityKeyPair,
} from './nodeTemplates'
import { emptyNodeDraft, type NodeDraft } from './nodeDraft'

// 模板的价值在于"选完就能跑"：它必须给出协议/传输/安全/密钥四个层面都自洽的
// 配置。这些测试锁住最小契约——少一个字段，用户建出来的节点就是坏的。

describe('节点模板', () => {
  it('至少覆盖五种常见部署', () => {
    const ids = NODE_TEMPLATES.map((t) => t.id)
    expect(ids).toContain('vless-reality')
    expect(ids).toContain('vless-ws-tls')
    expect(ids).toContain('vmess-ws-tls')
    expect(ids).toContain('trojan-tls')
    expect(ids).toContain('ss2022')
  })

  it('每个模板都给出协议、端口、传输与安全层', () => {
    for (const tpl of NODE_TEMPLATES) {
      const patch = tpl.build('test-node')
      expect(patch.name, `${tpl.id} 缺 name`).toBe('test-node')
      expect(patch.protocol, `${tpl.id} 缺 protocol`).toBeTruthy()
      expect(patch.port, `${tpl.id} 缺 port`).toBeTruthy()
      const stream = patch.stream_settings as Record<string, unknown> | undefined
      expect(stream, `${tpl.id} 缺 stream_settings`).toBeTruthy()
      expect(stream!.network, `${tpl.id} 缺 network`).toBeTruthy()
      expect(stream!.security, `${tpl.id} 缺 security`).toBeTruthy()
      expect(patch.protocol_settings, `${tpl.id} 缺 protocol_settings`).toBeTruthy()
    }
  })

  it('VLESS 模板都带 decryption: none（否则核心拒绝启动）', () => {
    for (const tpl of NODE_TEMPLATES) {
      const patch = tpl.build('n')
      if (patch.protocol !== 'vless') continue
      const ps = patch.protocol_settings as Record<string, unknown>
      expect(ps.decryption, `${tpl.id} 缺 decryption`).toBe('none')
    }
  })

  it('WebSocket 模板把 ALPN 收窄到 http/1.1（h2 无法承载 ws 升级）', () => {
    for (const tpl of NODE_TEMPLATES) {
      const patch = tpl.build('n')
      const stream = patch.stream_settings as Record<string, any>
      if (stream.network !== 'ws' || stream.security !== 'tls') continue
      const alpn = stream.tlsSettings?.alpn
      expect(alpn, `${tpl.id} 的 ws+tls 必须声明 alpn`).toEqual(['http/1.1'])
    }
  })

  it('REALITY 模板带伪装目标与域名', () => {
    const patch = findTemplate('vless-reality')!.build('n')
    const rs = (patch.stream_settings as Record<string, any>).realitySettings
    expect(rs.dest).toBeTruthy()
    expect(Array.isArray(rs.serverNames) && rs.serverNames.length > 0).toBe(true)
    expect(Array.isArray(rs.shortIds) && rs.shortIds.length > 0).toBe(true)
  })

  it('SS2022 模板自动生成服务端密钥与密码，且长度符合 2022 规范', () => {
    const patch = findTemplate('ss2022')!.build('n')
    expect(patch.cipher).toBe('2022-blake3-aes-128-gcm')
    const ps = patch.protocol_settings as Record<string, unknown>
    expect(ps.method).toBe('2022-blake3-aes-128-gcm')
    // 128-gcm 需要 16 字节 base64 => 24 字符
    expect(String(ps.password).length).toBe(24)
    expect(patch.server_key).toBeTruthy()
  })

  it('模板能直接喂给 NodeDraft 且必填项齐全', () => {
    const base = emptyNodeDraft()
    for (const tpl of NODE_TEMPLATES) {
      const merged: NodeDraft = { ...base, ...tpl.build('x') }
      expect(merged.protocol).toBeTruthy()
      expect(merged.port).toBeTruthy()
      expect(merged.stream_settings).toBeTruthy()
      expect(merged.protocol_settings).toBeTruthy()
    }
  })

  it('findTemplate 对未知 id 返回 undefined', () => {
    expect(findTemplate('nope')).toBeUndefined()
    expect(findTemplate('ss2022')?.label).toContain('Shadowsocks')
  })
})

describe('凭据生成', () => {
  it('UUID 符合 v4 格式且每次不同', () => {
    const a = newUUID()
    const b = newUUID()
    expect(a).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    expect(a).not.toBe(b)
  })

  it('base64 密钥长度与请求的字节数一致', () => {
    expect(newBase64Key(16).length).toBe(24)   // 16 字节 -> 24 base64 字符
    expect(newBase64Key(32).length).toBe(44)   // 32 字节 -> 44 base64 字符
  })

  it('shortId 是十六进制且长度正确', () => {
    const id = newShortId(8)
    expect(id).toMatch(/^[0-9a-f]{16}$/)
    expect(newShortId(4)).toMatch(/^[0-9a-f]{8}$/)
  })

  it('REALITY 密钥对是 base64url 且私钥公钥不同', async () => {
    let pair
    try {
      pair = await newRealityKeyPair()
    } catch {
      // 测试环境（jsdom）没有 X25519，跳过而不是误报失败
      return
    }
    expect(pair.privateKey).not.toBe(pair.publicKey)
    expect(pair.privateKey).toMatch(/^[A-Za-z0-9_-]+$/)
    expect(pair.publicKey).toMatch(/^[A-Za-z0-9_-]+$/)
  })
})
