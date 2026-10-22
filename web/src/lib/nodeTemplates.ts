/**
 * 节点预设模板与凭据生成。
 *
 * 目的：让"新建节点"从"填 20 多个不认识的字段"变成"选一个模板、点一下生成、
 * 改个名字"。3x-ui 这类面板的核心易用性就在这里。
 *
 * 每个模板给出的是**一套能直接跑通的配置**，用户只需要补 host / port 和
 * 服务器，其余都有可用默认值。
 */
import type { NodeDraft } from './nodeDraft'

export interface NodeTemplate {
  id: string
  /** 展示名，用户看到的名字。 */
  label: string
  /** 一句话说明适用场景。 */
  description: string
  /** 这个模板需要用户额外准备什么（提示用）。 */
  needs: string
  /** 生成一份 draft 补丁；name 由调用方决定。 */
  build(name: string): Partial<NodeDraft>
}

/** 生成 UUID v4。crypto.randomUUID 在非安全上下文不可用，做降级。 */
export function newUUID(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  const bytes = new Uint8Array(16)
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
    crypto.getRandomValues(bytes)
  } else {
    for (let i = 0; i < 16; i += 1) bytes[i] = Math.floor(Math.random() * 256)
  }
  bytes[6] = (bytes[6]! & 0x0f) | 0x40
  bytes[8] = (bytes[8]! & 0x3f) | 0x80
  const hex = [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

/** 生成 base64 随机字节，用于 Shadowsocks 密码 / PSK。 */
export function newBase64Key(bytes: number): string {
  const buf = new Uint8Array(bytes)
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
    crypto.getRandomValues(buf)
  } else {
    for (let i = 0; i < bytes; i += 1) buf[i] = Math.floor(Math.random() * 256)
  }
  let bin = ''
  for (const b of buf) bin += String.fromCharCode(b)
  return btoa(bin)
}

/** 生成 X25519 密钥对（REALITY 用）。返回 base64url 编码，与 Xray 的格式一致。 */
export async function newRealityKeyPair(): Promise<{ privateKey: string; publicKey: string }> {
  // 优先用 WebCrypto 生成 X25519，再按 Xray 的裸 32 字节 base64url 编码。
  // 环境不支持时抛错，由调用方提示用户手填或改用后端生成。
  const subtle = globalThis.crypto?.subtle
  if (!subtle) throw new Error('当前浏览器不支持 WebCrypto，无法自动生成 REALITY 密钥')

  const pair = (await subtle.generateKey({ name: 'X25519' } as AlgorithmIdentifier, true, [
    'deriveBits',
  ])) as CryptoKeyPair

  const rawPriv = await subtle.exportKey('pkcs8', pair.privateKey)
  const rawPub = await subtle.exportKey('raw', pair.publicKey)

  // pkcs8 的尾部 32 字节就是原始私钥
  const priv = new Uint8Array(rawPriv).slice(-32)
  const pub = new Uint8Array(rawPub).slice(-32)

  const toB64Url = (u8: Uint8Array) => {
    let bin = ''
    for (const b of u8) bin += String.fromCharCode(b)
    return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  }
  return { privateKey: toB64Url(priv), publicKey: toB64Url(pub) }
}

/** 生成短的十六进制 ID（REALITY shortId 用，0–8 字节）。 */
export function newShortId(bytes = 8): string {
  const buf = new Uint8Array(bytes)
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
    crypto.getRandomValues(buf)
  } else {
    for (let i = 0; i < bytes; i += 1) buf[i] = Math.floor(Math.random() * 256)
  }
  return [...buf].map((b) => b.toString(16).padStart(2, '0')).join('')
}

/**
 * 五个常用模板，覆盖绝大多数实际部署。
 *
 * 每个模板都给出可运行的完整配置；用户在表单里只需要改「名称、端口、域名」。
 */
export const NODE_TEMPLATES: NodeTemplate[] = [
  {
    id: 'vless-reality',
    label: 'VLESS + REALITY',
    description: '当前最推荐的方式。不需要自己的域名和证书，借用别人的站点做伪装。',
    needs: '需要填一个用于伪装的域名（如 www.microsoft.com）和 443 端口。',
    build(name) {
      return {
        name,
        port: '443',
        protocol: 'vless',
        flow: 'xtls-rprx-vision',
        stream_settings: {
          network: 'tcp',
          security: 'reality',
          realitySettings: {
            dest: 'www.microsoft.com:443',
            serverNames: ['www.microsoft.com'],
            shortIds: [newShortId(8)],
          },
        },
        protocol_settings: { decryption: 'none' },
        client_settings: {},
      }
    },
  },
  {
    id: 'vless-ws-tls',
    label: 'VLESS + WebSocket + TLS',
    description: '走 443 的 WebSocket，可以套 CDN。需要自己的域名和证书。',
    needs: '需要域名和证书文件（或在「加密层」里贴自签证书）。',
    build(name) {
      return {
        name,
        port: '443',
        protocol: 'vless',
        flow: '',
        stream_settings: {
          network: 'ws',
          security: 'tls',
          wsSettings: { path: '/ws', headers: {} },
          tlsSettings: { alpn: ['http/1.1'] },
        },
        protocol_settings: { decryption: 'none' },
        client_settings: {},
      }
    },
  },
  {
    id: 'vmess-ws-tls',
    label: 'VMess + WebSocket + TLS',
    description: '兼容性最好的组合，客户端支持最广。同样需要域名和证书。',
    needs: '需要域名和证书文件。',
    build(name) {
      return {
        name,
        port: '443',
        protocol: 'vmess',
        flow: '',
        stream_settings: {
          network: 'ws',
          security: 'tls',
          wsSettings: { path: '/vm', headers: {} },
          tlsSettings: { alpn: ['http/1.1'] },
        },
        protocol_settings: { security: 'auto' },
        client_settings: {},
      }
    },
  },
  {
    id: 'trojan-tls',
    label: 'Trojan + TLS',
    description: '伪装成正常的 HTTPS 流量。需要域名和证书。',
    needs: '需要域名和证书文件。',
    build(name) {
      return {
        name,
        port: '443',
        protocol: 'trojan',
        flow: '',
        stream_settings: {
          network: 'tcp',
          security: 'tls',
          tlsSettings: { alpn: ['http/1.1'] },
        },
        protocol_settings: {},
        client_settings: {},
      }
    },
  },
  {
    id: 'ss2022',
    label: 'Shadowsocks 2022',
    description: '轻量、开销小，不需要 TLS。适合自己用或小范围分享。',
    needs: '不需要域名和证书，密码会自动生成。',
    build(name) {
      return {
        name,
        port: '8388',
        protocol: 'shadowsocks',
        flow: '',
        cipher: '2022-blake3-aes-128-gcm',
        server_key: newBase64Key(16),
        stream_settings: { network: 'tcp', security: 'none' },
        protocol_settings: {
          method: '2022-blake3-aes-128-gcm',
          password: newBase64Key(16),
        },
        client_settings: {},
      }
    },
  },
]

/** 按 id 取模板。 */
export function findTemplate(id: string): NodeTemplate | undefined {
  return NODE_TEMPLATES.find((t) => t.id === id)
}
