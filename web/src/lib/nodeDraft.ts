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
import { STREAM_FIELDS, type StreamField, type StreamValue } from './streamSchema'
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
  /** 只读的脱敏密钥状态，不进入写请求。 */
  server_key_set?: boolean
  sort_order: string
  stream_settings: JsonObject
  protocol_settings: JsonObject
  client_settings: JsonObject
  /**
   * 入站级对象，与 Xray / 3x-ui 同名同层：sniffing / allocate / externalProxy。
   * 它们既不在 streamSettings 里，也不在 settings 里，所以要单独持久化。
   */
  sniffing: JsonObject
  allocate: JsonObject
  external_proxy: unknown[]
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
    sniffing: {},
    allocate: {},
    external_proxy: [],
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
    server_key_set: node.server_key_set === true,
    sort_order: String(node.sort_order ?? 0),
    stream_settings: cloneJson(node.stream_settings ?? {}),
    protocol_settings: cloneJson(node.protocol_settings ?? {}),
    client_settings: cloneJson(node.client_settings ?? {}),
    sniffing: cloneJson(node.sniffing ?? {}),
    allocate: cloneJson(node.allocate ?? {}),
    external_proxy: (cloneJson(node.external_proxy ?? []) as unknown[]) ?? [],
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

/**
 * 结构化视图：network / security 加上 streamSchema 声明的全部字段。
 * 文本/数字/列表统一是 string（输入框直接绑定），布尔是 boolean。
 * 具体键由 streamSchema 生成，改字段时两边要一起改（有单元测试核对）。
 */
export interface StreamView {
  network: string
  security: string
  tcpHeaderType: string
  wsPath: string
  wsHost: string
  wsMaxEarlyData: string
  wsEarlyDataHeaderName: string
  grpcServiceName: string
  grpcAuthority: string
  huPath: string
  huHost: string
  xhttpPath: string
  xhttpHost: string
  xhttpMode: string
  kcpMtu: string
  kcpTti: string
  kcpUplink: string
  kcpDownlink: string
  kcpReadBuffer: string
  kcpWriteBuffer: string
  kcpHeaderType: string
  kcpSeed: string
  tlsServerName: string
  tlsAlpn: string
  tlsMinVersion: string
  tlsMaxVersion: string
  tlsCipherSuites: string
  tlsFingerprint: string
  tlsAllowInsecure: string
  tlsEchConfigList: string
  tlsCertFile: string
  tlsKeyFile: string
  realityDest: string
  realityXver: string
  realityServerNames: string
  realityShortIds: string
  realityPrivateKey: string
  realityMinClientVer: string
  realityMaxClientVer: string
  realityMaxTimeDiff: string
  realityMldsa65Seed: string
  sockTcpFastOpen: string
  sockTproxy: string
  sockDomainStrategy: string
  sockDialerProxy: string
  sockTcpKeepAliveIdle: string
  sockTcpKeepAliveInterval: string
  sockTcpUserTimeout: string
  sockTcpcongestion: string
  sockTcpMaxSeg: string
  sockMark: string
  sockInterface: string
  sockTcpWindowClamp: string
  acceptProxyProtocol: boolean
  grpcMultiMode: boolean
  kcpCongestion: boolean
  tlsRejectUnknownSni: boolean
  realityShow: boolean
  sockTcpMptcp: boolean
  sockV6Only: boolean
  /** 声明之外的键仍可按 StreamValue 访问（用于 schema 驱动渲染）。 */
  [key: string]: StreamValue
}


/** 读取：按 paths 顺序回退，兼容旧位置与别名。 */
export function streamView(stream: JsonObject): StreamView {
  const view: StreamView = {
    network: String(stream.network ?? 'tcp'),
    security: String(stream.security ?? 'none'),
  } as StreamView
  // Xray 26 把 tcp 传输层改名为 raw；视图里保留用户写下的名字，只在比较时归一化。
  for (const field of STREAM_FIELDS) {
    view[field.key] = readField(stream, field)
  }
  // 证书文件在 certificates[] 数组里，不按点路径寻址。
  const cert = firstCertificate(stream)
  view.tlsCertFile = String(cert.certificateFile ?? getPath(stream, 'tlsSettings.certificateFile') ?? '')
  view.tlsKeyFile = String(cert.keyFile ?? getPath(stream, 'tlsSettings.keyFile') ?? '')
  return view
}

function firstCertificate(stream: JsonObject): JsonObject {
  const existing = getPath(stream, 'tlsSettings.certificates')
  return Array.isArray(existing) && isRecord(existing[0]) ? (existing[0] as JsonObject) : {}
}

function readField(stream: JsonObject, field: StreamField): StreamValue {
  for (const path of field.paths) {
    const raw = getPath(stream, path)
    if (raw === undefined || raw === null) continue
    if (field.kind === 'bool') return raw === true
    if (field.kind === 'list') return joinList(raw)
    if (field.kind === 'number') return typeof raw === 'number' ? String(raw) : String(raw)
    if (raw === '') continue
    return String(raw)
  }
  return field.kind === 'bool' ? false : ''
}

/**
 * 选择写入路径。
 *
 * rawSettings 是 Xray 26 的正式名，tcpSettings 是旧名。节点里已经有 tcpSettings
 * 时继续写它，避免一次保存就产生两个同义块（核心不保证哪个生效）；新节点写
 * rawSettings。acceptProxyProtocol 的旧 sockopt 位置只读不写 —— 那是历史上
 * 放错的地方，写入统一到传输层自己的块里。
 */
function writerPath(stream: JsonObject, field: StreamField): string {
  const primary = field.paths[0]
  if (!primary.startsWith('rawSettings.')) return primary
  const hasRaw = getPath(stream, 'rawSettings') !== undefined
  const hasLegacy = getPath(stream, 'tcpSettings') !== undefined
  if (hasRaw || !hasLegacy) return primary
  const suffix = primary.slice('rawSettings.'.length)
  return `tcpSettings.${suffix}`
}

/** 写入一个字段的规范化值；空字符串表示删除该键。 */
function writeField(stream: JsonObject, field: StreamField, value: StreamValue): JsonObject {
  const path = writerPath(stream, field)
  if (field.kind === 'bool') {
    // 布尔字段永远显式写 true/false：省略和 false 在 Xray 里语义不同，
    // 省略会退回默认（例如 acceptProxyProtocol 默认 false，但显式 false 更可读）。
    return setPath(stream, path, value === true)
  }
  const text = String(value ?? '').trim()
  // target 与 dest 在 Xray 里互为别名。只写正名，并删掉旧别名，避免同一份
  // 配置里出现两个取值不同的别名（核心不保证哪个生效）。
  if (field.key === 'realityDest') {
    const cleared = setPath(stream, 'realitySettings.dest', undefined)
    return text === '' ? setPath(cleared, path, undefined) : setPath(cleared, path, text)
  }
  if (text === '') return setPath(stream, path, undefined)
  if (field.kind === 'number' || field.numeric) {
    // xver / tcpFastOpen 这类 select 的取值必须是数字，写成字符串核心会解析失败。
    const n = Number(text)
    return setPath(stream, path, Number.isFinite(n) ? n : text)
  }
  if (field.kind === 'list') {
    // shortIds 允许空字符串作为「空 shortId」条目，不能用通用的 splitList 丢掉它。
    return setPath(stream, path, field.key === 'realityShortIds' ? splitShortIds(text) : splitList(text))
  }
  return setPath(stream, path, text)
}

/**
 * 把结构化视图写回 stream_settings，保留其它键（fallbacks、未声明的原生键等）。
 * 切换 security 不删除另一分支的配置块；需要清理时由 UI 显式调用 clearSecurityBlock。
 */
export function applyStreamView(stream: JsonObject, view: StreamView): JsonObject {
  const before = streamView(stream)
  // 旧版本把客户端侧的 REALITY 参数写进 realitySettings.settings（Xray 没有这个
  // 字段）。即使可见字段没变也要修掉，所以这里不能提前 return。
  let next = cloneJson(stream)
  if (JSON.stringify(before) !== JSON.stringify(view)) {
    next = setOrDelete(next, 'network', view.network)
    next = setOrDelete(next, 'security', view.security)
    const security = String(view.security || 'none')
    for (const field of STREAM_FIELDS) {
      if (view[field.key] === before[field.key]) continue
      // 证书走 certificates[] 数组，由 applyCertificateFiles 处理。
      if (isCertificateField(field)) continue
      // TLS/REALITY 只写自己那一块，避免切换 security 时把另一分支的配置也改掉。
      const inScope = field.section === 'transport' || field.section === 'sockopt'
        || (field.section === 'tls' && security === 'tls')
        || (field.section === 'reality' && security === 'reality')
      if (!inScope) continue
      next = writeField(next, field, view[field.key])
    }
    // TLS 证书走 certificates[] 数组，不是普通标量键。
    if (security === 'tls') next = applyCertificateFiles(next, view, before)
    if (security === 'tcp' || security === 'none' || true) {
      // 清理空的 sockopt 容器，避免写出 "sockopt": {}
      const sockopt = getPath(next, 'sockopt')
      if (isRecord(sockopt) && Object.keys(sockopt).length === 0) next = setPath(next, 'sockopt', undefined)
    }
  }
  if (securityOf(view) === 'reality' && getPath(next, 'realitySettings.settings') !== undefined) {
    // 客户端侧参数属于 client_settings；这层是历史遗留的死结构，无条件清掉。
    next = setPath(next, 'realitySettings.settings', undefined)
  }
  return next
}

function isCertificateField(field: StreamField): boolean {
  return field.key === 'tlsCertFile' || field.key === 'tlsKeyFile'
}

function securityOf(view: StreamView): string {
  return String(view.security || 'none')
}

/**
 * 证书文件写在 tlsSettings.certificates[0]，其余数组项原样保留。
 * 旧数据可能把 certificateFile/keyFile 放在 tlsSettings 顶层，读取已回退兼容。
 */
function applyCertificateFiles(next: JsonObject, view: StreamView, before: StreamView): JsonObject {
  if (view.tlsCertFile === before.tlsCertFile && view.tlsKeyFile === before.tlsKeyFile) return next
  const existing = getPath(next, 'tlsSettings.certificates')
  const template = Array.isArray(existing) && isRecord(existing[0]) ? (existing[0] as JsonObject) : {}
  const cert: JsonObject = { ...template }
  if (String(view.tlsCertFile ?? '').trim()) cert.certificateFile = String(view.tlsCertFile).trim()
  else delete cert.certificateFile
  if (String(view.tlsKeyFile ?? '').trim()) cert.keyFile = String(view.tlsKeyFile).trim()
  else delete cert.keyFile
  const remaining = Array.isArray(existing) ? existing.slice(1) : []
  if (Object.keys(cert).length > 0) return setPath(next, 'tlsSettings.certificates', [cert, ...remaining])
  return setPath(next, 'tlsSettings.certificates', remaining.length ? remaining : undefined)
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
  if (JSON.stringify(protocolView(settings, protocol)) === JSON.stringify(view)) return cloneJson(settings)
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
  spiderX: string
  alpn: string
  allowInsecure: string
}

export function clientView(settings: JsonObject): ClientView {
  return {
    serverName: String(settings.server_name ?? settings.serverName ?? settings.sni ?? ''),
    publicKey: String(settings.public_key ?? settings.publicKey ?? ''),
    spiderX: String(settings.spider_x ?? settings.spiderX ?? ''),
    shortId: String(settings.short_id ?? settings.shortId ?? ''),
    fingerprint: String(settings.fingerprint ?? ''),
    alpn: joinList(settings.alpn),
    allowInsecure: typeof (settings.allow_insecure ?? settings.allowInsecure) === 'boolean' ? String(settings.allow_insecure ?? settings.allowInsecure) : '',
  }
}

export function applyClientView(settings: JsonObject, view: ClientView): JsonObject {
  const before = clientView(settings)
  let next = cloneJson(settings)
  const update = (field: keyof ClientView, keys: string[], value: unknown) => {
    if (view[field] === before[field]) return
    const key = keys.find(key => key in settings) ?? keys[keys.length - 1]!
    next = setOrDelete(next, key, value)
    // 删除冲突别名，否则后端可能优先读取旧值。
    for (const alias of keys) if (alias !== key) next = setPath(next, alias, undefined)
  }
  update('serverName', ['server_name', 'sni', 'serverName'], view.serverName)
  update('publicKey', ['public_key', 'publicKey'], view.publicKey)
  update('shortId', ['short_id', 'shortId'], view.shortId)
  update('fingerprint', ['fingerprint'], view.fingerprint)
  update('alpn', ['alpn'], view.alpn ? splitList(view.alpn) : undefined)
  update('allowInsecure', ['allow_insecure', 'allowInsecure'], view.allowInsecure === '' ? undefined : view.allowInsecure === 'true')
  return next
}

export function protocolKnownKeys(protocol: NodeProtocol): string[] {
  if (protocol === 'vless') return ['decryption', 'fallbacks']
  if (protocol === 'trojan') return ['fallbacks']
  if (protocol === 'shadowsocks') return ['method', 'password', 'network']
  return ['disableInsecureEncryption']
}

/**
 * 结构化表单能编辑（或明确保留）的 stream_settings 顶层键。
 * 其余键会作为「原样保留」提示出来；这里漏一个就会把用户自己写的配置误报成未知。
 */
export function streamKnownKeys(): string[] {
  return [
    'network', 'security',
    'rawSettings', 'tcpSettings', 'wsSettings', 'grpcSettings',
    'httpupgradeSettings', 'xhttpSettings', 'kcpSettings',
    'tlsSettings', 'realitySettings', 'sockopt',
  ]
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
  // 对外端口可选：留空表示与监听端口相同。填了就必须是合法端口，
  // 且不能等于监听端口 —— 相同说明没配对，几乎总是误填。
  const rawExternal = draft.external_port.trim()
  if (rawExternal) {
    const externalPort = Number(rawExternal)
    if (!Number.isInteger(externalPort) || externalPort < 1 || externalPort > 65535) {
      err('external_port', '对外端口必须是 1–65535 的整数（留空表示与监听端口相同）')
    } else if (Number.isInteger(port) && externalPort === port) {
      err('external_port', '对外端口不能与监听端口相同；若两者一致，请留空')
    }
  }
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
    if (!view.realityPrivateKey.trim() && !draft.server_key.trim() && !draft.server_key_set) err('reality-private-key', 'REALITY 需要私钥，可点「生成一对」自动生成')
    if ((view.realityPrivateKey.trim() || draft.server_key.trim()) && !String(draft.client_settings.public_key ?? draft.client_settings.publicKey).trim()) {
      err('reality-public-key', '更换 REALITY 密钥时私钥与公钥必须成对填写')
    }
    if (!view.realityShortIds.trim()) warn('reality-short-ids', '没填短 ID；客户端可能连不上，可以点「生成」')
    if (!clientView(draft.client_settings).publicKey.trim()) warn('client-public-key', '客户端公钥为空，成员配置会不可用')
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
    sniffing: cloneJson(draft.sniffing),
    allocate: cloneJson(draft.allocate),
    external_proxy: cloneJson(draft.external_proxy),
    sort_order: Number(draft.sort_order),
  }
  if (!options.omitServerKey) payload.server_key = draft.server_key.trim()
  return { payload, errors: [], warnings }
}

/** 换钥必须由显式授权触发；set 标志不是私钥，旧公钥不算新配对输入。 */
export function realityRotationError(draft: NodeDraft, original: NodeRecord | null, rotate: boolean): string {
  if (!original || streamView(draft.stream_settings).security !== 'reality') return ''
  const privateKey = streamView(draft.stream_settings).realityPrivateKey.trim() || draft.server_key.trim()
  const publicKey = clientView(draft.client_settings).publicKey.trim()
  const previousPublic = clientView(original.client_settings).publicKey
  if (!rotate && (privateKey || publicKey !== previousPublic)) return '更换 REALITY 私钥或公钥前必须显式勾选「更换 REALITY 密钥」'
  if (rotate && (!privateKey || !publicKey || publicKey === previousPublic)) return '更换 REALITY 密钥必须填写一对新的私钥与公钥（可点「生成一对」）'
  return ''
}

/** 编辑页摘要：协议 / 传输 / 安全，列表页复用。 */
export function nodeSummary(node: NodeRecord): string {
  const network = String(node.stream_settings?.network ?? 'tcp')
  const security = String(node.stream_settings?.security ?? 'none')
  return `${node.protocol.toUpperCase()} · ${network}${security === 'none' ? '' : ` · ${security.toUpperCase()}`}`
}