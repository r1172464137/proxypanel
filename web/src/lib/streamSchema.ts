/**
 * Xray 入站配置字段表 —— 按 3x-ui 的入站表单逐项对齐。
 *
 * 为什么用声明式表：这些字段有 40 多个，散落在 template 里既容易漏、又容易
 * 读写路径不一致。这里一处声明「标签 / 写入路径 / 读取回退路径 / 类型」，
 * 由 nodeDraft 负责序列化，NodeEditorView 负责渲染。
 *
 * 三条硬约束：
 * 1. 写入只写 canonical 路径（paths[0]）；读取按顺序回退，兼容旧数据与别名。
 * 2. 结构化表单只碰已知路径；未声明的键原样保留（与 stream_settings 的约定一致）。
 * 3. 空字符串表示「删除该键」，不是写入空值 —— 否则会把默认值固化成显式配置。
 */

export type StreamValue = string | boolean
export type StreamView = Record<string, StreamValue>

export type StreamFieldKind = 'text' | 'password' | 'number' | 'list' | 'bool' | 'select'
export type StreamSection = 'transport' | 'tls' | 'reality' | 'sockopt'

export interface StreamField {
  /** 视图里的键名，也是检测「用户是否改过」的比较依据。 */
  key: string
  label: string
  kind: StreamFieldKind
  /** [写入路径, ...读取回退路径]。 */
  paths: readonly string[]
  section: StreamSection
  options?: readonly string[]
  hint?: string
  placeholder?: string
  /** select 的取值是数字（如 xver、tcpFastOpen），写回时要转成 number。 */
  numeric?: boolean
  /** 只在该传输层下显示；缺省表示所有传输层都显示。 */
  networks?: readonly string[]
  /** 只在指定 security 下显示。 */
  securities?: readonly string[]
}

export const NETWORK_OPTIONS = ['tcp', 'raw', 'ws', 'grpc', 'httpupgrade', 'xhttp', 'kcp'] as const

/** Xray 26 把 TCP 传输层改名为 raw，两者互为别名。 */
export const NETWORK_ALIASES: Record<string, string> = { mkcp: 'kcp', tcp: 'raw' }

export const SECURITY_OPTIONS = ['none', 'tls', 'reality'] as const

export const TLS_VERSIONS = ['', '1.0', '1.1', '1.2', '1.3'] as const

/** kcp header 伪装类型，3x-ui 的完整列表。 */
export const KCP_HEADERS = ['none', 'srtp', 'utp', 'wechat-video', 'dtls', 'wireguard'] as const

export const XHTTP_MODES = ['auto', 'packet-up', 'stream-up', 'stream-one'] as const

export const DOMAIN_STRATEGIES = [
  'AsIs', 'UseIP', 'UseIPv4', 'UseIPv6', 'UseIPv4v6', 'UseIPv6v4',
  'ForceIP', 'ForceIPv4', 'ForceIPv6', 'ForceIPv4v6', 'ForceIPv6v4',
] as const

/** TCP Fast Open：'' = 不设置（跟随系统），其余写进 sockopt.tcpFastOpen。 */
export const TFO_OPTIONS = ['', '0', '1', '256', '4096'] as const

export const TPROXY_OPTIONS = ['', 'off', 'redirect', 'tproxy'] as const

export const STREAM_FIELDS: readonly StreamField[] = [
  // ── 传输层：RAW / TCP ───────────────────────────────────────────────────
  // PROXY protocol 写在 rawSettings（3x-ui 与官方 RawObject 的主推位置），
  // 读取时兼容旧版本写过的 tcpSettings 与 sockopt 两处。
  {
    key: 'acceptProxyProtocol', label: '接收 PROXY protocol', kind: 'bool',
    paths: ['rawSettings.acceptProxyProtocol', 'tcpSettings.acceptProxyProtocol', 'sockopt.acceptProxyProtocol'],
    section: 'transport', networks: ['tcp', 'raw'],
    hint: '只有前置 nginx / HAProxy 真的发送 PROXY 头时才开；开了之后不带头的连接会被直接关闭。',
  },
  {
    key: 'tcpHeaderType', label: 'TCP 伪装头', kind: 'select',
    paths: ['rawSettings.header.type', 'tcpSettings.header.type'], options: ['none', 'http'],
    section: 'transport', networks: ['tcp', 'raw'],
    hint: 'http 伪装必须与客户端一致；HTTP 服务器无法按此分流，只有 VLESS fallbacks 可以。',
  },

  // ── 传输层：WebSocket ───────────────────────────────────────────────────
  {
    key: 'wsPath', label: '路径', kind: 'text', paths: ['wsSettings.path'],
    section: 'transport', networks: ['ws'], placeholder: '/ws',
  },
  {
    key: 'wsHost', label: 'Host 头', kind: 'text', paths: ['wsSettings.headers.Host'],
    section: 'transport', networks: ['ws'], placeholder: 'example.com',
  },
  {
    key: 'wsMaxEarlyData', label: 'maxEarlyData', kind: 'number', paths: ['wsSettings.maxEarlyData'],
    section: 'transport', networks: ['ws'], hint: '0 表示关闭 0-RTT。',
  },
  {
    key: 'wsEarlyDataHeaderName', label: 'earlyDataHeaderName', kind: 'text',
    paths: ['wsSettings.earlyDataHeaderName'], section: 'transport', networks: ['ws'],
    placeholder: 'Sec-WebSocket-Protocol',
  },

  // ── 传输层：gRPC ────────────────────────────────────────────────────────
  {
    key: 'grpcServiceName', label: 'serviceName', kind: 'text', paths: ['grpcSettings.serviceName'],
    section: 'transport', networks: ['grpc'], placeholder: 'GunService',
  },
  {
    key: 'grpcAuthority', label: 'authority', kind: 'text', paths: ['grpcSettings.authority'],
    section: 'transport', networks: ['grpc'],
  },
  {
    key: 'grpcMultiMode', label: 'multiMode', kind: 'bool', paths: ['grpcSettings.multiMode'],
    section: 'transport', networks: ['grpc'],
  },

  // ── 传输层：HTTPUpgrade ─────────────────────────────────────────────────
  {
    key: 'huPath', label: '路径', kind: 'text', paths: ['httpupgradeSettings.path'],
    section: 'transport', networks: ['httpupgrade'], placeholder: '/upgrade',
  },
  {
    key: 'huHost', label: 'Host 头', kind: 'text', paths: ['httpupgradeSettings.host'],
    section: 'transport', networks: ['httpupgrade'],
  },

  // ── 传输层：XHTTP ───────────────────────────────────────────────────────
  {
    key: 'xhttpPath', label: '路径', kind: 'text', paths: ['xhttpSettings.path'],
    section: 'transport', networks: ['xhttp'], placeholder: '/xhttp',
  },
  {
    key: 'xhttpHost', label: 'Host 头', kind: 'text', paths: ['xhttpSettings.host'],
    section: 'transport', networks: ['xhttp'],
  },
  {
    key: 'xhttpMode', label: '模式', kind: 'select', paths: ['xhttpSettings.mode'],
    options: XHTTP_MODES, section: 'transport', networks: ['xhttp'],
  },

  // ── 传输层：mKCP ────────────────────────────────────────────────────────
  { key: 'kcpMtu', label: 'mtu', kind: 'number', paths: ['kcpSettings.mtu'], section: 'transport', networks: ['kcp'] },
  { key: 'kcpTti', label: 'tti', kind: 'number', paths: ['kcpSettings.tti'], section: 'transport', networks: ['kcp'] },
  { key: 'kcpUplink', label: 'uplinkCapacity', kind: 'number', paths: ['kcpSettings.uplinkCapacity'], section: 'transport', networks: ['kcp'] },
  { key: 'kcpDownlink', label: 'downlinkCapacity', kind: 'number', paths: ['kcpSettings.downlinkCapacity'], section: 'transport', networks: ['kcp'] },
  { key: 'kcpCongestion', label: 'congestion', kind: 'bool', paths: ['kcpSettings.congestion'], section: 'transport', networks: ['kcp'] },
  { key: 'kcpReadBuffer', label: 'readBufferSize', kind: 'number', paths: ['kcpSettings.readBufferSize'], section: 'transport', networks: ['kcp'] },
  { key: 'kcpWriteBuffer', label: 'writeBufferSize', kind: 'number', paths: ['kcpSettings.writeBufferSize'], section: 'transport', networks: ['kcp'] },
  {
    key: 'kcpHeaderType', label: '伪装头', kind: 'select', paths: ['kcpSettings.header.type'],
    options: KCP_HEADERS, section: 'transport', networks: ['kcp'],
  },
  { key: 'kcpSeed', label: 'seed', kind: 'password', paths: ['kcpSettings.seed'], section: 'transport', networks: ['kcp'] },

  // ── TLS ─────────────────────────────────────────────────────────────────
  {
    key: 'tlsServerName', label: 'serverName', kind: 'text', paths: ['tlsSettings.serverName'],
    section: 'tls', securities: ['tls'], hint: 'SNI。留空时客户端使用节点 host。',
  },
  {
    key: 'tlsAlpn', label: 'ALPN', kind: 'list', paths: ['tlsSettings.alpn'],
    section: 'tls', securities: ['tls'], placeholder: 'h2, http/1.1',
  },
  {
    key: 'tlsMinVersion', label: '最低版本', kind: 'select', paths: ['tlsSettings.minVersion'],
    options: TLS_VERSIONS, section: 'tls', securities: ['tls'],
  },
  {
    key: 'tlsMaxVersion', label: '最高版本', kind: 'select', paths: ['tlsSettings.maxVersion'],
    options: TLS_VERSIONS, section: 'tls', securities: ['tls'],
  },
  {
    key: 'tlsCipherSuites', label: 'cipherSuites', kind: 'list', paths: ['tlsSettings.cipherSuites'],
    section: 'tls', securities: ['tls'], hint: '仅 TLS 1.2 及以下生效，1.3 的套件不可配置。',
  },
  {
    key: 'tlsRejectUnknownSni', label: '拒绝未知 SNI', kind: 'bool', paths: ['tlsSettings.rejectUnknownSni'],
    section: 'tls', securities: ['tls'],
  },
  {
    key: 'tlsFingerprint', label: 'uTLS 指纹', kind: 'text', paths: ['tlsSettings.settings.fingerprint'],
    section: 'tls', securities: ['tls'], placeholder: 'chrome',
  },
  {
    key: 'tlsAllowInsecure', label: '跳过证书校验', kind: 'select',
    paths: ['tlsSettings.settings.allowInsecure'], options: ['', 'false', 'true'],
    section: 'tls', securities: ['tls'],
  },
  {
    key: 'tlsEchConfigList', label: 'ECH 配置', kind: 'text', paths: ['tlsSettings.settings.echConfigList'],
    section: 'tls', securities: ['tls'],
  },
  {
    key: 'tlsCertFile', label: '证书文件', kind: 'text', paths: ['tlsSettings.certificates.0.certificateFile', 'tlsSettings.certificateFile'],
    section: 'tls', securities: ['tls'], placeholder: '/etc/xray/fullchain.pem',
  },
  {
    key: 'tlsKeyFile', label: '私钥文件', kind: 'text', paths: ['tlsSettings.certificates.0.keyFile', 'tlsSettings.keyFile'],
    section: 'tls', securities: ['tls'], placeholder: '/etc/xray/privkey.pem',
  },

  // ── REALITY ─────────────────────────────────────────────────────────────
  {
    key: 'realityShow', label: '调试日志', kind: 'bool', paths: ['realitySettings.show'],
    section: 'reality', securities: ['reality'], hint: '会打印 REALITY 调试信息，仅排障时开启。',
  },
  {
    key: 'realityDest', label: '伪装目标', kind: 'text', paths: ['realitySettings.target', 'realitySettings.dest'],
    section: 'reality', securities: ['reality'], placeholder: 'www.microsoft.com:443',
    hint: 'target 是正名，dest 是旧别名。建议用一个正常的公网 TLS 站点。',
  },
  {
    key: 'realityXver', label: 'PROXY protocol 版本', kind: 'select', paths: ['realitySettings.xver'],
    options: ['', '0', '1', '2'], numeric: true, section: 'reality', securities: ['reality'],
    hint: '转发到伪装目标时补的 PROXY 头版本；目标强制 PROXY protocol 时必须设 1 或 2。',
  },
  {
    key: 'realityServerNames', label: '允许的域名', kind: 'list', paths: ['realitySettings.serverNames'],
    section: 'reality', securities: ['reality'], placeholder: 'www.microsoft.com',
  },
  {
    key: 'realityShortIds', label: 'shortIds', kind: 'list', paths: ['realitySettings.shortIds'],
    section: 'reality', securities: ['reality'], placeholder: '0123abcd, ""',
    hint: '短 ID 用来区分同一域名下的不同节点；留空表示只接受空 shortId。',
  },
  {
    key: 'realityPrivateKey', label: '私钥', kind: 'password', paths: ['realitySettings.privateKey'],
    section: 'reality', securities: ['reality'], hint: '留空则由「服务端密钥」自动生成，公钥会自动推导。',
  },
  {
    key: 'realityMinClientVer', label: 'minClientVer', kind: 'text', paths: ['realitySettings.minClientVer'],
    section: 'reality', securities: ['reality'], placeholder: '1.8.0',
  },
  {
    key: 'realityMaxClientVer', label: 'maxClientVer', kind: 'text', paths: ['realitySettings.maxClientVer'],
    section: 'reality', securities: ['reality'],
  },
  {
    key: 'realityMaxTimeDiff', label: 'maxTimeDiff（毫秒）', kind: 'number', paths: ['realitySettings.maxTimeDiff'],
    section: 'reality', securities: ['reality'],
  },
  {
    key: 'realityMldsa65Seed', label: 'mldsa65Seed', kind: 'password', paths: ['realitySettings.mldsa65Seed'],
    section: 'reality', securities: ['reality'], hint: '后量子签名种子，仅在对端支持时填写。',
  },

  // ── sockopt ─────────────────────────────────────────────────────────────
  {
    key: 'sockTcpFastOpen', label: 'tcpFastOpen', kind: 'select', paths: ['sockopt.tcpFastOpen'],
    options: TFO_OPTIONS, numeric: true, section: 'sockopt',
  },
  {
    key: 'sockTproxy', label: 'tproxy', kind: 'select', paths: ['sockopt.tproxy'],
    options: TPROXY_OPTIONS, section: 'sockopt',
  },
  {
    key: 'sockDomainStrategy', label: 'domainStrategy', kind: 'select', paths: ['sockopt.domainStrategy'],
    options: ['', ...DOMAIN_STRATEGIES], section: 'sockopt',
  },
  {
    key: 'sockDialerProxy', label: 'dialerProxy', kind: 'text', paths: ['sockopt.dialerProxy'],
    section: 'sockopt', hint: '链式代理的出站 tag。',
  },
  { key: 'sockTcpKeepAliveIdle', label: 'tcpKeepAliveIdle', kind: 'number', paths: ['sockopt.tcpKeepAliveIdle'], section: 'sockopt' },
  { key: 'sockTcpKeepAliveInterval', label: 'tcpKeepAliveInterval', kind: 'number', paths: ['sockopt.tcpKeepAliveInterval'], section: 'sockopt' },
  { key: 'sockTcpUserTimeout', label: 'tcpUserTimeout', kind: 'number', paths: ['sockopt.tcpUserTimeout'], section: 'sockopt' },
  {
    key: 'sockTcpcongestion', label: 'tcpcongestion', kind: 'select', paths: ['sockopt.tcpcongestion'],
    options: ['', 'bbr', 'cubic', 'reno'], section: 'sockopt',
  },
  { key: 'sockTcpMptcp', label: 'tcpMptcp', kind: 'bool', paths: ['sockopt.tcpMptcp'], section: 'sockopt' },
  { key: 'sockTcpMaxSeg', label: 'tcpMaxSeg', kind: 'number', paths: ['sockopt.tcpMaxSeg'], section: 'sockopt' },
  { key: 'sockMark', label: 'mark', kind: 'number', paths: ['sockopt.mark'], section: 'sockopt', hint: '仅 Linux，需要 CAP_NET_ADMIN。' },
  { key: 'sockInterface', label: 'interface', kind: 'text', paths: ['sockopt.interface'], section: 'sockopt' },
  { key: 'sockV6Only', label: 'V6Only', kind: 'bool', paths: ['sockopt.V6Only'], section: 'sockopt' },
  { key: 'sockTcpWindowClamp', label: 'tcpWindowClamp', kind: 'number', paths: ['sockopt.tcpWindowClamp'], section: 'sockopt' },
]

/** 真实 REALITY 交换需要 target/serverNames/私钥；这些在 UI 上单独提示。 */
export function fieldsFor(section: StreamSection, network: string, security: string): StreamField[] {
  const net = NETWORK_ALIASES[network] ?? network
  return STREAM_FIELDS.filter((f) => {
    if (f.section !== section) return false
    if (f.networks && !f.networks.includes(net) && !f.networks.includes(network)) return false
    if (f.securities && !f.securities.includes(security)) return false
    return true
  })
}
