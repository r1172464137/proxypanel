import { describe, expect, it } from 'vitest'
import type { NodeRecord } from '@/api/types'
import {
  applyClientView,
  applyProtocolView,
  applyStreamView,
  buildNodeWrite,
  clientView,
  draftFromNode,
  emptyNodeDraft,
  protocolView,
  removeSecurityBlock,
  realityRotationError,
  streamView,
  validateNodeDraft,
  type NodeDraft,
} from './nodeDraft'
import { getPath, setPath } from './json'

function vlessNode(): NodeRecord {
  return {
    id: 'nd_1',
    version: 7,
    name: 'Tokyo VLESS',
    server_id: 'srv_1',
    enabled: true,
    host: 'node.example.com',
    port: 443,
    listen: '0.0.0.0',
    protocol: 'vless',
    flow: 'xtls-rprx-vision',
    cipher: null,
    server_key: 'private-key',
    stream_settings: {
      network: 'tcp',
      security: 'reality',
      sockopt: { tcpFastOpen: true, mark: 255 },
      realitySettings: {
        dest: 'www.example.com:443',
        serverNames: ['www.example.com'],
        privateKey: 'reality-private',
        shortIds: ['ab12', ''],
        settings: { publicKey: 'reality-public', fingerprint: 'chrome', spiderX: '/' },
      },
    },
    protocol_settings: {
      decryption: 'none',
      fallbacks: [{ dest: 80, alpn: '' }],
      // 后端注入字段：必须被剥离，不能由管理员提交共享 clients
      clients: [{ id: 'should-not-be-sent' }],
    },
    client_settings: {
      serverName: 'www.example.com',
      publicKey: 'reality-public',
      shortId: 'ab12',
      fingerprint: 'chrome',
      alpn: ['h2', 'http/1.1'],
      mux: { enabled: true },
    },
    sort_order: 3,
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-02T00:00:00Z',
  }
}

describe('nodeDraft 结构化视图', () => {
  it('从节点读取结构化字段，保留原生 JSON', () => {
    const draft = draftFromNode(vlessNode())
    const view = streamView(draft.stream_settings)
    expect(view.network).toBe('tcp')
    expect(view.security).toBe('reality')
    expect(view.realityDest).toBe('www.example.com:443')
    expect(view.realityShortIds).toBe('ab12, ')
    expect(view.realityPublicKey).toBe('reality-public')
    expect(draft.stream_settings.sockopt).toEqual({ tcpFastOpen: true, mark: 255 })

    const client = clientView(draft.client_settings)
    expect(client.serverName).toBe('www.example.com')
    expect(client.alpn).toBe('h2, http/1.1')
    expect(draft.client_settings.mux).toEqual({ enabled: true })
  })

  it('applyStreamView 只写已知字段，切换 security 不删除另一分支，清理需显式', () => {
    const base = vlessNode().stream_settings
    const changed = applyStreamView(base, { ...streamView(base), network: 'ws' })
    expect(changed.network).toBe('ws')
    // The server-side REALITY block is preserved. The legacy nested
    // realitySettings.settings layer is dropped on save: Xray's RealityObject
    // has no such field, and the client values live in client_settings.
    const { settings: legacy, ...realityWithoutLegacy } = base.realitySettings as Record<string, unknown>
    expect(legacy).toBeDefined()
    expect(changed.realitySettings).toEqual(realityWithoutLegacy)
    expect(changed.sockopt).toEqual({ tcpFastOpen: true, mark: 255 })

    const cleared = removeSecurityBlock(changed, 'reality')
    expect(cleared.realitySettings).toBeUndefined()
    expect(cleared.sockopt).toEqual({ tcpFastOpen: true, mark: 255 })
  })

  it('保存时清除旧版 realitySettings.settings，并支持 PROXY protocol 与 header', () => {
    const base = vlessNode().stream_settings
    const legacy = applyStreamView(base, streamView(base))
    expect((legacy.realitySettings as Record<string, unknown>).settings).toBeUndefined()

    const view = streamView(base)
    const enabled = applyStreamView(base, { ...view, acceptProxyProtocol: true, tcpHeaderType: 'http' })
    expect((enabled.sockopt as Record<string, unknown>).acceptProxyProtocol).toBe(true)
    // Existing sockopt keys survive.
    expect((enabled.sockopt as Record<string, unknown>).mark).toBe(255)
    expect((enabled.tcpSettings as Record<string, Record<string, unknown>>).header.type).toBe('http')

    const disabled = applyStreamView(enabled, { ...streamView(enabled), acceptProxyProtocol: false })
    expect((disabled.sockopt as Record<string, unknown>).acceptProxyProtocol).toBeUndefined()
    expect((disabled.sockopt as Record<string, unknown>).mark).toBe(255)
  })

  it('applyProtocolView 保留 fallbacks 与未知字段', () => {
    const settings = vlessNode().protocol_settings
    const view = protocolView(settings, 'vless')
    const next = applyProtocolView(settings, 'vless', { ...view, decryption: 'none' })
    expect(next.fallbacks).toEqual([{ dest: 80, alpn: '' }])
    expect(next.clients).toBeDefined()
  })

  it('applyClientView 保留未知客户端键', () => {
    const next = applyClientView(vlessNode().client_settings, {
      ...clientView(vlessNode().client_settings),
      shortId: 'ff00',
    })
    expect(next.shortId).toBe('ff00')
    expect(next.mux).toEqual({ enabled: true })
  })
})

describe('无损与脱敏反例', () => {
  it('只改名经结构化往返，多证书、空列表、未知嵌套和传输参数深等', () => {
    const record = vlessNode()
    const stream = {
      network: 'xhttp', security: 'tls',
      xhttpSettings: { path: '/edge', mode: 'auto', extra: { xPaddingBytes: '100-1000' } },
      sockopt: { tcpKeepAliveIdle: 60 },
      tlsSettings: { alpn: [], certificates: [
        { certificate: ['PEM-A'], key: ['KEY-A'], usage: 'encipherment', future: { retain: true } },
        { certificateFile: '/b.pem', keyFile: '/b.key', oneTimeLoading: true },
      ], rejectUnknownSni: true },
    }
    const client = { server_name: 'edge.example', alpn: [], allow_insecure: false, future: { nested: [1, 2] } }
    record.stream_settings = stream
    record.client_settings = client
    record.protocol_settings = { decryption: 'none', fallbacks: [], future: { retain: true } }
    const draft = draftFromNode(record)
    draft.name = 'renamed only'
    draft.stream_settings = applyStreamView(draft.stream_settings, streamView(draft.stream_settings))
    draft.protocol_settings = applyProtocolView(draft.protocol_settings, draft.protocol, protocolView(draft.protocol_settings, draft.protocol))
    draft.client_settings = applyClientView(draft.client_settings, clientView(draft.client_settings))
    const result = buildNodeWrite(draft, { omitServerKey: true })
    expect(result.errors).toEqual([])
    expect(result.payload?.stream_settings).toEqual(stream)
    expect(result.payload?.protocol_settings).toEqual({ decryption: 'none', fallbacks: [], future: { retain: true } })
    expect(result.payload?.client_settings).toEqual(client)
    expect(result.payload).not.toHaveProperty('server_key')
  })

  it('改TLS域名不改证书，security=none与空shortId保持原生形状', () => {
    const tls = { network: 'tcp', security: 'tls', tlsSettings: { serverName: 'old', alpn: [], certificates: [{ certificate: ['A'], key: ['B'] }, { certificateFile: '/b' }] } }
    expect(applyStreamView(tls, { ...streamView(tls), tlsServerName: 'new' })).toEqual({ ...tls, tlsSettings: { ...tls.tlsSettings, serverName: 'new' } })
    const none = { network: 'ws', security: 'none', wsSettings: { path: '/x', headers: { Host: 'example' } } }
    expect(applyStreamView(none, streamView(none))).toEqual(none)
    const reality = { network: 'tcp', security: 'reality', realitySettings: { target: 'example:443', shortIds: [''], serverNames: ['example'] } }
    expect(applyStreamView(reality, streamView(reality))).toEqual(reality)
  })

  it('canonical客户端键可读可改，不制造优先级冲突，其他字段不动', () => {
    const original = { server_name: 'old', public_key: 'public', short_id: '', allow_insecure: false, alpn: [] }
    expect(clientView(original).publicKey).toBe('public')
    expect(applyClientView(original, { ...clientView(original), serverName: 'new' })).toEqual({ ...original, server_name: 'new' })
  })

  it('脱敏REALITY密钥留空保持；换钥需显式授权和新的私公钥输入', () => {
    const record = vlessNode()
    record.server_key_set = true
    record.stream_settings = setPath(record.stream_settings, 'realitySettings.privateKey', undefined)
    const draft = draftFromNode(record)
    expect(draft.server_key).toBe('')
    expect(validateNodeDraft(draft).errors).toEqual([])
    expect(realityRotationError(draft, record, false)).toBe('')
    expect(realityRotationError(draft, record, true)).toContain('一对新的')
    draft.server_key = 'new-private-fixture'
    expect(realityRotationError(draft, record, false)).toContain('显式勾选')
    expect(realityRotationError(draft, record, true)).toContain('一对新的')
    draft.client_settings.publicKey = 'new-public-fixture'
    expect(realityRotationError(draft, record, true)).toBe('')
    draft.server_key = ''
    expect(realityRotationError(draft, record, true)).toContain('一对新的')
  })
})

describe('buildNodeWrite', () => {
  it('完整提交三个对象，保留未知字段并剥离 clients', () => {
    const draft = draftFromNode(vlessNode())
    const result = buildNodeWrite(draft)
    expect(result.errors).toEqual([])
    const payload = result.payload!
    expect(payload.stream_settings).toEqual({
      network: 'tcp',
      security: 'reality',
      sockopt: { tcpFastOpen: true, mark: 255 },
      realitySettings: vlessNode().stream_settings.realitySettings,
    })
    expect(payload.client_settings).toEqual(vlessNode().client_settings)
    expect(payload.protocol_settings.clients).toBeUndefined()
    expect(payload.protocol_settings.fallbacks).toEqual([{ dest: 80, alpn: '' }])
    // 服务端 GET 不返回明文 server_key，draft 从空值开始；只在用户显式填写时提交。
    expect(payload.server_key).toBe('')
    expect(result.warnings.some((warning) => warning.includes('clients'))).toBe(true)
  })

  it('omitServerKey 时不提交 server_key（保持现有密钥）', () => {
    const draft = draftFromNode(vlessNode())
    const result = buildNodeWrite(draft, { omitServerKey: true })
    expect(result.payload).not.toBeNull()
    expect('server_key' in result.payload!).toBe(false)
  })

  it('校验拦截错误端口与缺失必填', () => {
    const draft = emptyNodeDraft()
    draft.listen = ''
    const result = buildNodeWrite(draft)
    expect(result.payload).toBeNull()
    expect(result.errors).toContain('节点名称必填')
    expect(result.errors).toContain('必须选择所属服务器')
    expect(result.errors.some((e) => e.includes('对外地址必填'))).toBe(true)
    expect(result.errors).toContain('监听地址必填')
    expect(result.errors.some((error) => error.includes('端口'))).toBe(false) // 默认 443 合法
    draft.port = '70000'
    expect(buildNodeWrite(draft).errors.some((error) => error.includes('端口'))).toBe(true)
  })

  it('REALITY 只允许 VLESS，并要求 dest/serverNames/privateKey', () => {
    const draft: NodeDraft = emptyNodeDraft()
    draft.name = 'n'
    draft.server_id = 'srv_1'
    draft.host = 'h'
    draft.stream_settings = setPath(draft.stream_settings, 'security', 'reality')
    const errors = validateNodeDraft(draft).errors
    expect(errors.some((error) => error.includes('伪装目标'))).toBe(true)
    expect(errors.some((error) => error.includes('允许的域名'))).toBe(true)
    expect(errors.some((error) => error.includes('私钥'))).toBe(true)

    draft.protocol = 'vmess'
    expect(validateNodeDraft(draft).errors.some((e) => e.includes('REALITY 只能配 VLESS'))).toBe(true)
  })

  it('Shadowsocks 2022 要求方法，密钥留空给警告而非静默', () => {
    const draft = emptyNodeDraft()
    draft.name = 'ss'
    draft.server_id = 'srv_1'
    draft.host = 'h'
    draft.protocol = 'shadowsocks'
    draft.protocol_settings = {}
    draft.client_settings = {}
    expect(validateNodeDraft(draft).errors.some((error) => error.includes('加密算法'))).toBe(true)

    draft.protocol_settings = { method: '2022-blake3-aes-128-gcm' }
    const validation = validateNodeDraft(draft)
    expect(validation.errors).toEqual([])
    expect(validation.warnings.some((w) => w.includes('服务端密钥'))).toBe(true)
  })

  it('fallbacks 非数组被阻止，未知协议键被提示保留', () => {
    const draft = draftFromNode(vlessNode())
    draft.protocol_settings = setPath(draft.protocol_settings, 'fallbacks', { dest: 80 })
    const errors = validateNodeDraft(draft).errors
    expect(errors.some((e) => e.includes('必须是 JSON 数组'))).toBe(true)

    draft.protocol_settings = setPath(vlessNode().protocol_settings, 'customField', 1)
    const warnings = validateNodeDraft(draft).warnings
    expect(warnings.some((warning) => warning.includes('customField'))).toBe(true)
  })

  it('realitySettings.xver 由原生 JSON 提供时不会被结构化编辑清掉', () => {
    // xver 决定 REALITY 转发到 dest 时是否补 PROXY protocol 头。面板目前没有
    // 这个字段的控件，所以用户只能写进原生 JSON；保存路径必须原样保留它。
    const stream = setPath(vlessNode().stream_settings, 'realitySettings.xver', 1)
    const view = streamView(stream)
    const saved = applyStreamView(stream, { ...view, realityDest: '127.0.0.1:8443' })
    expect(getPath(saved, 'realitySettings.xver')).toBe(1)
    expect(getPath(saved, 'realitySettings.dest')).toBe('127.0.0.1:8443')
  })
})
