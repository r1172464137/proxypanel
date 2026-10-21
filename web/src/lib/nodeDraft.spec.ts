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
  streamView,
  validateNodeDraft,
  type NodeDraft,
} from './nodeDraft'
import { setPath } from './json'

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
    expect(changed.realitySettings).toEqual(base.realitySettings)
    expect(changed.sockopt).toEqual({ tcpFastOpen: true, mark: 255 })

    const cleared = removeSecurityBlock(changed, 'reality')
    expect(cleared.realitySettings).toBeUndefined()
    expect(cleared.sockopt).toEqual({ tcpFastOpen: true, mark: 255 })
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
    expect(result.errors).toContain('对外地址（host）必填')
    expect(result.errors).toContain('监听地址（listen）必填')
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
    expect(errors.some((error) => error.includes('dest'))).toBe(true)
    expect(errors.some((error) => error.includes('serverNames'))).toBe(true)
    expect(errors.some((error) => error.includes('privateKey'))).toBe(true)

    draft.protocol = 'vmess'
    expect(validateNodeDraft(draft).errors).toContain('REALITY 仅支持 VLESS 入站')
  })

  it('Shadowsocks 2022 要求方法，密钥留空给警告而非静默', () => {
    const draft = emptyNodeDraft()
    draft.name = 'ss'
    draft.server_id = 'srv_1'
    draft.host = 'h'
    draft.protocol = 'shadowsocks'
    draft.protocol_settings = {}
    draft.client_settings = {}
    expect(validateNodeDraft(draft).errors.some((error) => error.includes('加密方法'))).toBe(true)

    draft.protocol_settings = { method: '2022-blake3-aes-128-gcm' }
    const validation = validateNodeDraft(draft)
    expect(validation.errors).toEqual([])
    expect(validation.warnings.some((warning) => warning.includes('2022'))).toBe(true)
  })

  it('fallbacks 非数组被阻止，未知协议键被提示保留', () => {
    const draft = draftFromNode(vlessNode())
    draft.protocol_settings = setPath(draft.protocol_settings, 'fallbacks', { dest: 80 })
    const errors = validateNodeDraft(draft).errors
    expect(errors).toContain('fallbacks 必须是 JSON 数组')

    draft.protocol_settings = setPath(vlessNode().protocol_settings, 'customField', 1)
    const warnings = validateNodeDraft(draft).warnings
    expect(warnings.some((warning) => warning.includes('customField'))).toBe(true)
  })
})