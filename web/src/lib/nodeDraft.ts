/**
 * 节点表单逻辑（纯函数，可测试）。
 *
 * 关键约束（implementation-contract.md）：
 * - stream_settings / protocol_settings / client_settings 是 Xray 原生结构；
 *   结构化编辑器只写已知路径，其余键原样保留，不静默丢弃。
 * - clients 由后端注入，前端提交前显式移除并告知（不能绕过授权共享客户端）。
 * - 服务端私钥/密钥只用于管理端，不进入成员配置。
 */
import { cloneJson, getPath, isRecord, parseJsonObject, setPath, unknownKeys, type JsonObject } from './json'
import type { NodeProtocol, NodeRecord, NodeWrite } from '@/api/types'

export const NETWORK_OPTIONS = ['tcp', 'ws', 'grpc', 'httpupgrade', 'xhttp'] as const
export const SECURITY_OPTIONS = ['none', 'tls', 'reality'] as const
export const SS_METHOD_OPTIONS = [
  'aes-128-gcm',
  'aes-256-gcm',
  'chacha20-poly1305',
  '2022-blake3-aes-128-gcm',
  '2022-blake3-aes-256-gcm',
  '2022-blake3-chacha20-poly1305',
] as const

export interface NodeDraft {
  id: string | null
  version: number | null
  name: string
  server_id: string
  enabled: boolean
  host: string
  port: string
  /** 对外端口（客户端连接的）；留空表示与 port 相同。 */
  external_port: string
  listen: string
  protocol: NodeProtocol
  flow: string
  cipher: string
  server_key: string
  sort_order: string
  stream_settings: JsonObject
  protocol_settings: JsonObject
  client_settings: JsonObject
}

export function emptyNodeDraft(): NodeDraft {
  return {
    id: null,
    version: null,
    name: '',
    server_id: '',
    enabled: true,
    host: '',
    port: '443',
    external_port: '',
    listen: '0.0.0.0',
    protocol: 'vless',
    flow: '',
    cipher: '',
    server_key: '',
    sort_order: '0',
    stream_settings: { network: 'tcp', security: 'none' },
    protocol_settings: { decryption: 'none' },
    client_settings: {},
  }
}

export function draftFromNode(node: NodeRecord): NodeDraft {
  return {
    id: node.id,
    version: node.version,
    name: node.name ?? '',
    server_id: node.server_id ?? '',
    enabled: node.enabled !== false,
    host: node.host ?? '',
    port: String(node.port ?? ''),
    external_port: node.external_port ? String(node.external_port) : '',
    listen: node.listen ?? '',
    protocol: (node.protocol ?? 'vless') as NodeProtocol,
    flow: node.flow ?? '',
    cipher: node.cipher ?? '',
    // 服务端不返回明文 server_key（只返回 server_key_set）；默认保持现有密钥。
    server_key: '',
    sort_order: String(node.sort_order ?? 0),
    stream_settings: cloneJson(node.stream_settings ?? {}),
    protocol_settings: cloneJson(node.protocol_settings ?? {}),
    client_settings: cloneJson(node.client_settings ?? {}),
  }
}

function splitList(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean)
}

/** shortIds 允许空字符串（REALITY 常用空 shortId），因此保留空段。 */
function splitShortIds(text: string): string[] | undefined {
  const parts = text.split(',').map((item) => item.trim())
  if (!parts.some(Boolean)) return undefined
  return parts
}

function joinList(value: unknown): string {
  if (Array.isArray(value)) return value.map((item) => String(item)).join(', ')
  if (typeof value === 'string') return value
  return ''
}

function setOrDelete(source: JsonObject, path: string, value: unknown): JsonObject {
  if (value === '' || value === undefined || value === null) return setPath(source, path, undefined)
  return setPath(source, path, value)
}

export interface StreamView {
  network: string
  security: string
  tlsServerName: string
  tlsAlpn: string
  tlsCertFile: string
  tlsKeyFile: string
  realityDest: string
  realityServerNames: string
  realityPrivateKey: string
  realityShortIds: string
  realityPublicKey: string
  realityFingerprint: string
  realitySpiderX: string
}

export function streamView(stream: JsonObject): StreamView {
  const certificates = getPath(stream, 'tlsSettings.certificates')
  const firstCert = Array.isArray(certificates) && isRecord(certificates[0]) ? (certificates[0] as JsonObject) : {}
  return {
    network: String(stream.network ?? 'tcp'),
    security: String(stream.security ?? 'none'),
    tlsServerName: String(getPath(stream, 'tlsSettings.serverName') ?? ''),
    tlsAlpn: joinList(getPath(stream, 'tlsSettings.alpn')),
    tlsCertFile: String(firstCert.certificateFile ?? getPath(stream, 'tlsSettings.certificateFile') ?? ''),
    tlsKeyFile: String(firstCert.keyFile ?? getPath(stream, 'tlsSettings.keyFile') ?? ''),
    realityDest: String(getPath(stream, 'realitySettings.dest') ?? getPath(stream, 'realitySettings.target') ?? ''),
    realityServerNames: joinList(getPath(stream, 'realitySettings.serverNames')),
    realityPrivateKey: String(getPath(stream, 'realitySettings.privateKey') ?? ''),
    realityShortIds: joinList(getPath(stream, 'realitySettings.shortIds')),
    realityPublicKey: String(getPath(stream, 'realitySettings.settings.publicKey') ?? ''),
    realityFingerprint: String(getPath(stream, 'realitySettings.settings.fingerprint') ?? ''),
    realitySpiderX: String(getPath(stream, 'realitySettings.settings.spiderX') ?? ''),
  }
}

/**
 * 把结构化视图写回 stream_settings，保留其它键（sockopt、wsSettings 等）。
 * 切换 security 不删除另一分支的配置块；需要清理时由 UI 显式调用 removeSecurityBlock。
 */
export function applyStreamView(stream: JsonObject, view: StreamView): JsonObject {
  let next = cloneJson(stream)
  next = setOrDelete(next, 'network', view.network)
  next = setOrDelete(next, 'security', view.security === 'none' ? undefined : view.security)

  if (view.security === 'tls') {
    next = setOrDelete(next, 'tlsSettings.serverName', view.tlsServerName)
    next = setOrDelete(next, 'tlsSettings.alpn', view.tlsAlpn ? splitList(view.tlsAlpn) : undefined)
    const existing = getPath(next, 'tlsSettings.certificates')
    const template = Array.isArray(existing) && isRecord(existing[0]) ? (existing[0] as JsonObject) : {}
    const cert: JsonObject = { ...template }
    if (view.tlsCertFile) cert.certificateFile = view.tlsCertFile
    else delete cert.certificateFile
    if (view.tlsKeyFile) cert.keyFile = view.tlsKeyFile
    else delete cert.keyFile
    if (Object.keys(cert).length > 0) next = setPath(next, 'tlsSettings.certificates', [cert])
    else next = setPath(next, 'tlsSettings.certificates', undefined)
  }
  if (view.security === 'reality') {
    next = setOrDelete(next, 'realitySettings.dest', view.realityDest)
    next = setOrDelete(next, 'realitySettings.serverNames', view.realityServerNames ? splitList(view.realityServerNames) : undefined)
    next = setOrDelete(next, 'realitySettings.privateKey', view.realityPrivateKey)
    next = setOrDelete(next, 'realitySettings.shortIds', splitShortIds(view.realityShortIds))
    next = setOrDelete(next, 'realitySettings.settings.publicKey', view.realityPublicKey)
    next = setOrDelete(next, 'realitySettings.settings.fingerprint', view.realityFingerprint)
    next = setOrDelete(next, 'realitySettings.settings.spiderX', view.realitySpiderX)
  }
  return next
}

/** 显式清理（用户点击按钮时调用），不是保存时的隐式行为。 */
export function removeSecurityBlock(stream: JsonObject, block: 'tls' | 'reality'): JsonObject {
  return setPath(stream, block === 'tls' ? 'tlsSettings' : 'realitySettings', undefined)
}

export interface ProtocolView {
  decryption: string
  fallbacksText: string
  ssMethod: string
  ssPassword: string
  ssNetwork: string
  vmessDisableInsecure: string
}

export function protocolView(settings: JsonObject, _protocol: NodeProtocol): ProtocolView {
  const disable = settings.disableInsecureEncryption
  return {
    decryption: String(settings.decryption ?? 'none'),
    fallbacksText: settings.fallbacks === undefined ? '' : JSON.stringify(settings.fallbacks, null, 2),
    ssMethod: String(settings.method ?? ''),
    ssPassword: String(settings.password ?? ''),
    ssNetwork: String(settings.network ?? ''),
    vmessDisableInsecure: typeof disable === 'boolean' ? String(disable) : '',
  }
}

/** 应用协议参数；fallbacks 文本必须是合法 JSON（由 validate 提前检查）。 */
export function applyProtocolView(
  settings: JsonObject,
  protocol: NodeProtocol,
  view: ProtocolView,
): JsonObject {
  let next = cloneJson(settings)
  if (protocol === 'vless') {
    next = setOrDelete(next, 'decryption', view.decryption)
  }
  if (protocol === 'vless' || protocol === 'trojan') {
    if (!view.fallbacksText.trim()) next = setPath(next, 'fallbacks', undefined)
    else {
      const parsed = parseJsonObject(`{"fallbacks":${view.fallbacksText}}`, 'fallbacks')
      if (parsed.ok) next = setPath(next, 'fallbacks', parsed.value.fallbacks)
    }
  }
  if (protocol === 'shadowsocks') {
    next = setOrDelete(next, 'method', view.ssMethod)
    next = setOrDelete(next, 'password', view.ssPassword)
    next = setOrDelete(next, 'network', view.ssNetwork)
  }
  if (protocol === 'vmess') {
    if (view.vmessDisableInsecure === '') next = setPath(next, 'disableInsecureEncryption', undefined)
    else next = setPath(next, 'disableInsecureEncryption', view.vmessDisableInsecure === 'true')
  }
  return next
}

export interface ClientView {
  serverName: string
  publicKey: string
  shortId: string
  fingerprint: string
  alpn: string
  allowInsecure: string
}

export function clientView(settings: JsonObject): ClientView {
  return {
    serverName: String(settings.serverName ?? settings.sni ?? ''),
    publicKey: String(settings.publicKey ?? ''),
    shortId: String(settings.shortId ?? ''),
    fingerprint: String(settings.fingerprint ?? ''),
    alpn: joinList(settings.alpn),
    allowInsecure: typeof settings.allowInsecure === 'boolean' ? String(settings.allowInsecure) : '',
  }
}

export function applyClientView(settings: JsonObject, view: ClientView): JsonObject {
  let next = cloneJson(settings)
  next = setOrDelete(next, 'serverName', view.serverName)
  next = setOrDelete(next, 'publicKey', view.publicKey)
  next = setOrDelete(next, 'shortId', view.shortId)
  next = setOrDelete(next, 'fingerprint', view.fingerprint)
  next = setOrDelete(next, 'alpn', view.alpn ? splitList(view.alpn) : undefined)
  next = setOrDelete(next, 'allowInsecure', view.allowInsecure === '' ? undefined : view.allowInsecure === 'true')
  return next
}

export function protocolKnownKeys(protocol: NodeProtocol): string[] {
  if (protocol === 'vless') return ['decryption', 'fallbacks']
  if (protocol === 'trojan') return ['fallbacks']
  if (protocol === 'shadowsocks') return ['method', 'password', 'network']
  return ['disableInsecureEncryption']
}

export function streamKnownKeys(): string[] {
  return ['network', 'security', 'tlsSettings', 'realitySettings']
}

export function clientKnownKeys(): string[] {
  return ['serverName', 'sni', 'publicKey', 'shortId', 'fingerprint', 'alpn', 'allowInsecure']
}

/**
 * 一条校验结果。
 *
 * field 是**表单元素的 id**（与模板里的 `id="node-name"` 对应），用来把错误
 * 定位到具体输入框。没有 field 的条目只出现在汇总区。
 */
export interface ValidationItem {
  field?: string
  message: string
}

export interface NodeValidation {
  errors: string[]
  warnings: string[]
  /** 与 errors 一一对应的结构化条目（含字段 id）。 */
  errorItems: ValidationItem[]
  /** 与 warnings 一一对应的结构化条目。 */
  warningItems: ValidationItem[]
}

export function validateNodeDraft(draft: NodeDraft): NodeValidation {
  const errorItems: ValidationItem[] = []
  const warningItems: ValidationItem[] = []
  const errors: string[] = []
  const warnings: string[] = []
  // err(field, message) / warn(field, message) 同时写入两条通道，
  // 汇总区仍然显示 errors/warnings 的纯文本。
  const err = (field: string | undefined, message: string) => {
    errors.push(message)
    errorItems.push({ field, message })
  }
  const warn = (field: string | undefined, message: string) => {
    warnings.push(message)
    warningItems.push({ field, message })
  }

  if (!draft.name.trim()) err('node-name', '节点名称必填')
  if (!draft.server_id.trim()) err('node-server', '必须选择所属服务器')
  if (!draft.host.trim()) err('node-host', '对外地址必填：成员客户端会连这个地址')
  const port = Number(draft.port)
  if (!Number.isInteger(port) || port < 1 || port > 65535) err('node-port', '端口必须是 1–65535 的整数')
  if (!draft.listen.trim()) err('node-listen', '监听地址必填')
  const sortOrder = Number(draft.sort_order)
  if (!Number.isInteger(sortOrder) || sortOrder < 0) err('node-sort', '排序值必须是非负整数')

  const network = String(draft.stream_settings.network ?? '')
  if (!network) err('stream-network', '传输方式必填（tcp / ws / grpc 等）')
  else if (!(NETWORK_OPTIONS as readonly string[]).includes(network)) {
    warn('stream-network', `传输 ${network} 不在常用选项中，将按原生 JSON 原样提交，请确认真实核心支持`)
  }

  const security = String(draft.stream_settings.security ?? 'none')
  if (security === 'reality' && draft.protocol !== 'vless') {
    err('node-protocol', 'REALITY 只能配 VLESS，请先把协议改成 VLESS')
  }
  if (security === 'reality') {
    const view = streamView(draft.stream_settings)
    if (!view.realityDest.trim()) err('reality-dest', 'REALITY 需要一个伪装目标，例如 www.microsoft.com:443')
    if (!view.realityServerNames.trim()) err('reality-server-names', 'REALITY 需要至少一个允许的域名')
    if (!view.realityPrivateKey.trim()) err('reality-private-key', 'REALITY 需要私钥，可点「生成一对」自动生成')
    if (!view.realityShortIds.trim()) warn('reality-short-ids', '没填短 ID；客户端可能连不上，可以点「生成」')
    if (!view.realityPublicKey.trim()) warn('client-public-key', '客户端公钥为空，成员配置会不可用')
  }
  if (security === 'tls') {
    const view = streamView(draft.stream_settings)
    if (!view.tlsCertFile || !view.tlsKeyFile) {
      warn('tls-cert-file', 'TLS 证书路径没填完整；证书必须在 Agent 的受控目录里')
    }
  }
  if (security !== 'tls' && security !== 'reality' && security !== 'none') {
    warn('stream-security', `加密层 ${security} 不在选项里，将按原始 JSON 提交`)
  }

  if (draft.protocol === 'vless') {
    if (!String(draft.protocol_settings.decryption ?? '').trim()) err('protocol-decryption', 'VLESS 需要填 decryption，通常是 none')
  }
  if (draft.protocol === 'shadowsocks') {
    const method = String(draft.protocol_settings.method ?? draft.cipher ?? '')
    if (!method) err('ss-method', 'Shadowsocks 需要选一个加密算法')
    if (method.startsWith('2022-')) {
      if (!String(draft.protocol_settings.password ?? '').trim()) {
        warn('ss-password', '没填服务端密钥；若不由后端注入，节点将无法启动')
      }
    } else if (method && !String(draft.protocol_settings.password ?? '').trim()) {
      warn('ss-password', '经典 Shadowsocks 通常需要密码')
    }
  }

  const fallbacks = protocolView(draft.protocol_settings, draft.protocol).fallbacksText
  if ((draft.protocol === 'vless' || draft.protocol === 'trojan') && fallbacks.trim()) {
    let parsed: unknown
    try {
      parsed = JSON.parse(fallbacks)
    } catch (caught) {
      err('protocol-fallbacks', `回落规则不是合法 JSON：${caught instanceof Error ? caught.message : String(caught)}`)
    }
    if (parsed !== undefined && !Array.isArray(parsed)) err('protocol-fallbacks', '回落规则必须是 JSON 数组，例如 []')
  }

  if (isRecord(draft.protocol_settings) && 'clients' in draft.protocol_settings) {
    warn(undefined, 'protocol_settings.clients 由后端注入：前端提交时会移除该字段，避免共享客户端凭据')
  }
  const unknownStream = unknownKeys(draft.stream_settings, streamKnownKeys())
  if (unknownStream.length) warn(undefined, `stream_settings 其它原生键将原样保留：${unknownStream.join(', ')}`)
  const unknownProtocol = unknownKeys(draft.protocol_settings, [...protocolKnownKeys(draft.protocol), 'clients'])
  if (unknownProtocol.length) warn(undefined, `protocol_settings 其它原生键将原样保留：${unknownProtocol.join(', ')}`)
  const unknownClient = unknownKeys(draft.client_settings, clientKnownKeys())
  if (unknownClient.length) warn(undefined, `client_settings 其它键将原样保留：${unknownClient.join(', ')}`)

  if (draft.server_key.trim()) {
    warnings.push('server_key 属于服务端秘密，只用于面板下发；不会写入成员配置')
  }

  return { errors, warnings, errorItems, warningItems }
}

export interface NodeBuildResult {
  payload: NodeWrite | null
  errors: string[]
  warnings: string[]
}

/**
 * 生成提交体。omitServerKey=true 时不下发 server_key（编辑时“保持现有密钥”）。
 * 协议 settings 中的 clients 一律移除并提示：授权客户端只能由后端注入。
 */
export function buildNodeWrite(draft: NodeDraft, options: { omitServerKey?: boolean } = {}): NodeBuildResult {
  const { errors, warnings } = validateNodeDraft(draft)
  if (errors.length) return { payload: null, errors, warnings }

  const protocol_settings = cloneJson(draft.protocol_settings)
  if ('clients' in protocol_settings) delete protocol_settings.clients

  const payload: NodeWrite = {
    name: draft.name.trim(),
    server_id: draft.server_id.trim(),
    enabled: draft.enabled,
    host: draft.host.trim(),
    port: Number(draft.port),
    external_port: draft.external_port.trim() ? Number(draft.external_port) : 0,
    listen: draft.listen.trim(),
    protocol: draft.protocol,
    flow: draft.flow.trim(),
    cipher: draft.cipher.trim(),
    stream_settings: cloneJson(draft.stream_settings),
    protocol_settings,
    client_settings: cloneJson(draft.client_settings),
    sort_order: Number(draft.sort_order),
  }
  if (!options.omitServerKey) payload.server_key = draft.server_key.trim()
  return { payload, errors: [], warnings }
}

/** 编辑页摘要：协议 / 传输 / 安全，列表页复用。 */
export function nodeSummary(node: NodeRecord): string {
  const network = String(node.stream_settings?.network ?? 'tcp')
  const security = String(node.stream_settings?.security ?? 'none')
  return `${node.protocol.toUpperCase()} · ${network}${security === 'none' ? '' : ` · ${security.toUpperCase()}`}`
}