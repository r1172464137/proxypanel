<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import type { NodeRecord, ServerRecord } from '@/api/types'
import { NODE_PROTOCOL_LABELS } from '@/api/types'
import {
  NETWORK_OPTIONS,
  SECURITY_OPTIONS,
  SS_METHOD_OPTIONS,
  applyClientView,
  applyProtocolView,
  applyStreamView,
  buildNodeWrite,
  clientKnownKeys,
  clientView,
  draftFromNode,
  emptyNodeDraft,
  protocolKnownKeys,
  protocolView,
  removeSecurityBlock,
  streamKnownKeys,
  streamView,
  validateNodeDraft,
  type ClientView,
  type NodeDraft,
  type ProtocolView,
  type StreamView,
} from '@/lib/nodeDraft'
import { unknownKeys, type JsonObject } from '@/lib/json'
import { errorText } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ErrorState from '@/components/ErrorState.vue'
import JsonEditor from '@/components/JsonEditor.vue'
import StatusBadge from '@/components/StatusBadge.vue'

const route = useRoute()
const router = useRouter()

const isEdit = computed(() => route.name === 'admin-node-edit' && typeof route.params.id === 'string')
const draft = reactive<NodeDraft>(emptyNodeDraft())
const streamState = reactive<StreamView>(streamView(draft.stream_settings))
const protocolState = reactive<ProtocolView>(protocolView(draft.protocol_settings, draft.protocol))
const clientState = reactive<ClientView>(clientView(draft.client_settings))

const node = ref<NodeRecord | null>(null)
const servers = ref<ServerRecord[]>([])
const serversError = ref<unknown>(null)
const loadError = ref<unknown>(null)
const loading = ref(false)
const saving = ref(false)
const saveError = ref('')
const jsonErrors = reactive({ stream: '', protocol: '', client: '' })
const keepServerKey = ref(true)
const tab = ref<'structured' | 'advanced'>('structured')

const serverOptions = computed(() =>
  servers.value.map((server) => ({ value: server.id, label: `${server.name} (${server.address})` })),
)

const preservedStream = computed(() => unknownKeys(draft.stream_settings, streamKnownKeys()))
const preservedProtocol = computed(() => unknownKeys(draft.protocol_settings, [...protocolKnownKeys(draft.protocol), 'clients']))
const preservedClient = computed(() => unknownKeys(draft.client_settings, clientKnownKeys()))
const hasClientsInSettings = computed(() => 'clients' in draft.protocol_settings)

const previewDraft = computed<NodeDraft>(() => ({
  ...draft,
  stream_settings: applyStreamView(draft.stream_settings, streamState),
  protocol_settings: applyProtocolView(draft.protocol_settings, draft.protocol, protocolState),
  client_settings: applyClientView(draft.client_settings, clientState),
}))

const validation = computed(() => validateNodeDraft(previewDraft.value))
const showFallbacks = computed(() => draft.protocol === 'vless' || draft.protocol === 'trojan')
const security = computed(() => String(draft.stream_settings.security ?? streamState.security ?? 'none'))

function syncDraft(): void {
  draft.stream_settings = applyStreamView(draft.stream_settings, streamState)
  draft.protocol_settings = applyProtocolView(draft.protocol_settings, draft.protocol, protocolState)
  draft.client_settings = applyClientView(draft.client_settings, clientState)
  if (draft.protocol === 'shadowsocks' && protocolState.ssMethod) draft.cipher = protocolState.ssMethod
}

watch(
  () => draft.stream_settings,
  (value) => Object.assign(streamState, streamView(value)),
  { deep: true },
)
watch(
  () => draft.protocol_settings,
  (value) => Object.assign(protocolState, protocolView(value, draft.protocol)),
  { deep: true },
)
watch(
  () => draft.client_settings,
  (value) => Object.assign(clientState, clientView(value)),
  { deep: true },
)
watch(
  () => draft.protocol,
  () => {
    Object.assign(protocolState, protocolView(draft.protocol_settings, draft.protocol))
    if (draft.protocol === 'shadowsocks' && !draft.cipher) draft.cipher = 'aes-256-gcm'
  },
)

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  serversError.value = null
  try {
    servers.value = (await adminApi.servers.list({ limit: 200 })).items
    if (!isEdit.value && servers.value.length === 1 && !draft.server_id) draft.server_id = servers.value[0]!.id
  } catch (caught) {
    serversError.value = caught
  }
  if (isEdit.value) {
    try {
      const record = await adminApi.nodes.get(String(route.params.id))
      node.value = record
      Object.assign(draft, draftFromNode(record))
      Object.assign(streamState, streamView(draft.stream_settings))
      Object.assign(protocolState, protocolView(draft.protocol_settings, draft.protocol))
      Object.assign(clientState, clientView(draft.client_settings))
      // 服务端不返回明文密钥，只返回 server_key_set；默认保持现有密钥。
      keepServerKey.value = record.server_key_set !== false
      if (!draft.cipher && record.protocol === 'shadowsocks') draft.cipher = protocolState.ssMethod || 'aes-256-gcm'
    } catch (caught) {
      loadError.value = caught
    }
  }
  loading.value = false
}

onMounted(() => void load())

async function save(): Promise<void> {
  saveError.value = ''
  if (jsonErrors.stream || jsonErrors.protocol || jsonErrors.client) {
    saveError.value = '高级 JSON 存在解析错误，已阻止提交以免丢失参数'
    tab.value = 'advanced'
    return
  }
  syncDraft()
  const result = buildNodeWrite(draft, { omitServerKey: isEdit.value && keepServerKey.value })
  if (!result.payload) {
    saveError.value = result.errors.join('；')
    return
  }
  saving.value = true
  try {
    if (isEdit.value && node.value) {
      await adminApi.nodes.update(node.value.id, { ...result.payload, version: node.value.version })
    } else {
      await adminApi.nodes.create(result.payload)
    }
    pushToast('success', '节点已保存', '节点技术配置变更默认重启该独立核心实例，连接会短暂中断；是否生效以任务回执为准。')
    await router.push('/admin/nodes')
  } catch (caught) {
    saveError.value = errorText(caught)
  } finally {
    saving.value = false
  }
}


function clearBlock(block: 'tls' | 'reality'): void {
  draft.stream_settings = removeSecurityBlock(draft.stream_settings, block)
}

function updateProtocolJson(value: JsonObject): void {
  draft.protocol_settings = value
}

function updateStreamJson(value: JsonObject): void {
  draft.stream_settings = value
}

function updateClientJson(value: JsonObject): void {
  draft.client_settings = value
}

const cipherValue = computed({
  get: () => protocolState.ssMethod || draft.cipher || '',
  set: (value: string) => {
    protocolState.ssMethod = value
    draft.cipher = value
  },
})
</script>

<template>
  <div>
    <PageHeader
      :title="isEdit ? '编辑节点' : '新建节点'"
      description="VLESS / VMess / Trojan / Shadowsocks（含 2022）。结构化表单与原生 JSON 编辑同一份数据：未知字段保留并在保存时原样提交。"
    >
      <router-link class="btn btn--sm" to="/admin/nodes">返回列表</router-link>
      <button type="button" class="btn btn--primary" :disabled="saving" @click="save">
        <span v-if="saving" class="spinner" aria-hidden="true" />
        {{ saving ? '保存中…' : '保存节点' }}
      </button>
    </PageHeader>

    <LoadingBlock v-if="loading" label="正在读取节点" />
    <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />
    <div v-else class="stack">
      <p v-if="saveError" class="banner banner--danger" role="alert">{{ saveError }}</p>
      <ErrorState v-if="serversError" :error="serversError" @retry="load" show-retry />
      <div v-if="!servers.length" class="banner banner--warn">
        还没有服务器档案，必须先创建服务器才能保存节点。<router-link to="/admin/servers">前往服务器</router-link>
      </div>
      <div v-if="node" class="banner banner--info">
        节点 ID <span class="mono">{{ node.id }}</span> · 版本 {{ node.version }} · 更新于 {{ formatDateTime(node.updated_at) }}
      </div>

      <section class="card">
        <div class="card__header"><h2>基本信息</h2></div>
        <div class="card__body grid grid--form">
          <div class="field">
            <label for="node-name">名称</label>
            <input id="node-name" v-model="draft.name" class="input" required />
          </div>
          <div class="field">
            <label for="node-server">所属服务器</label>
            <select id="node-server" v-model="draft.server_id" class="select">
              <option value="">请选择</option>
              <option v-for="option in serverOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
          </div>
          <div class="field">
            <label for="node-protocol">协议</label>
            <select id="node-protocol" v-model="draft.protocol" class="select">
              <option v-for="(label, value) in NODE_PROTOCOL_LABELS" :key="value" :value="value">{{ label }}</option>
            </select>
          </div>
          <div class="field">
            <label for="node-host">对外地址</label>
            <input id="node-host" v-model="draft.host" class="input mono" placeholder="节点域名或 IP（写入成员配置）" />
          </div>
          <div class="field">
            <label for="node-port">对外端口</label>
            <input id="node-port" v-model="draft.port" class="input" inputmode="numeric" />
          </div>
          <div class="field">
            <label for="node-listen">监听地址</label>
            <input id="node-listen" v-model="draft.listen" class="input mono" placeholder="0.0.0.0" />
          </div>
          <div class="field">
            <label for="node-sort">排序</label>
            <input id="node-sort" v-model="draft.sort_order" class="input" inputmode="numeric" />
          </div>
          <label class="checkbox" style="align-self: end; padding-bottom: 8px">
            <input v-model="draft.enabled" type="checkbox" />
            启用该节点（禁用会硬撤权并停止实例）
          </label>
        </div>
      </section>

      <div class="row">
        <button type="button" class="btn" :class="{ 'btn--primary': tab === 'structured' }" @click="tab = 'structured'">结构化参数</button>
        <button type="button" class="btn" :class="{ 'btn--primary': tab === 'advanced' }" @click="tab = 'advanced'">原生 JSON（高级）</button>
        <span class="text-xs text-muted">两种方式编辑同一对象；JSON 中的额外字段不会丢失。</span>
      </div>

      <template v-if="tab === 'structured'">
        <section class="card">
          <div class="card__header"><h2>协议参数（protocol_settings）</h2></div>
          <div class="card__body stack">
            <p v-if="hasClientsInSettings" class="banner banner--warn">
              protocol_settings 中存在 clients：客户端凭据只能由后端注入，保存时会移除该字段（其它字段保留）。
            </p>

            <div class="grid grid--form">
              <template v-if="draft.protocol === 'vless'">
                <div class="field">
                  <label for="protocol-decryption">加密方式</label>
                  <input id="protocol-decryption" v-model="protocolState.decryption" class="input mono" placeholder="none" />
                  <p class="field__help">VLESS 常用 none</p>
                </div>
                <div class="field">
                  <label for="vless-flow">流控</label>
                  <select id="vless-flow" v-model="draft.flow" class="select">
                    <option value="">不使用</option>
                    <option value="xtls-rprx-vision">xtls-rprx-vision</option>
                  </select>
                </div>
              </template>

              <template v-if="draft.protocol === 'vmess'">
                <div class="field">
                  <label for="vmess-insecure">禁用不安全加密</label>
                  <select id="vmess-insecure" v-model="protocolState.vmessDisableInsecure" class="select">
                    <option value="">不设置（沿用核心默认）</option>
                    <option value="true">true</option>
                    <option value="false">false</option>
                  </select>
                </div>
              </template>

              <template v-if="draft.protocol === 'shadowsocks'">
                <div class="field">
                  <label for="ss-method">加密算法</label>
                  <input id="ss-method" v-model="cipherValue" class="input mono" list="ss-methods" placeholder="aes-256-gcm 或 2022-blake3-*" />
                  <datalist id="ss-methods">
                    <option v-for="method in SS_METHOD_OPTIONS" :key="method" :value="method" />
                  </datalist>
                  <p class="field__help">2022 系列方法需配套多用户主密钥；被核心移除的算法不要声称支持。</p>
                </div>
                <div class="field">
                  <label for="ss-password">服务端密钥</label>
                  <input id="ss-password" v-model="protocolState.ssPassword" class="input mono" type="password" autocomplete="off" />
                  <p class="field__help">只保存在面板；不写入成员配置。留空表示由后端注入。</p>
                </div>
                <div class="field">
                  <label for="ss-network">传输网络</label>
                  <select id="ss-network" v-model="protocolState.ssNetwork" class="select">
                    <option value="">不设置</option>
                    <option value="tcp">tcp</option>
                    <option value="udp">udp</option>
                    <option value="both">both</option>
                  </select>
                </div>
              </template>
            </div>

            <div v-if="showFallbacks" class="field">
              <label for="protocol-fallbacks">回落规则（高级）</label>
              <textarea
                id="protocol-fallbacks"
                v-model="protocolState.fallbacksText"
                class="textarea"
                spellcheck="false"
                style="min-height: 120px"
                placeholder='[{"dest": 80, "alpn": ""}]'
              />
              <p class="field__help">与 Xray 原生 fallbacks 一致；留空表示不设置。非数组会在保存校验中被阻止。</p>
            </div>

            <div v-if="preservedProtocol.length" class="banner banner--info">
              以下 protocol_settings 键不在结构化表单中，将原样保留：<span class="mono">{{ preservedProtocol.join(', ') }}</span>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card__header">
            <h2>传输与安全（stream_settings）</h2>
            <div class="card__header-actions">
              <StatusBadge :status="security" :label="`security: ${security}`" />
            </div>
          </div>
          <div class="card__body stack">
            <div class="grid grid--form">
              <div class="field">
                <label for="stream-network">传输方式</label>
                <input id="stream-network" v-model="streamState.network" class="input mono" list="networks" />
                <datalist id="networks">
                  <option v-for="network in NETWORK_OPTIONS" :key="network" :value="network" />
                </datalist>
                <p class="field__help">常用 tcp / ws / grpc / httpupgrade；核心已移除的传输请勿声称支持。</p>
              </div>
              <div class="field">
                <label for="stream-security">加密层</label>
                <select id="stream-security" v-model="streamState.security" class="select">
                  <option v-for="option in SECURITY_OPTIONS" :key="option" :value="option">{{ option }}</option>
                </select>
              </div>
            </div>

            <template v-if="streamState.security === 'tls'">
              <div class="section-title">TLS</div>
              <div class="grid grid--form">
                <div class="field">
                  <label for="tls-sni">证书域名</label>
                  <input id="tls-sni" v-model="streamState.tlsServerName" class="input mono" />
                </div>
                <div class="field">
                  <label for="tls-alpn">协商协议</label>
                  <input id="tls-alpn" v-model="streamState.tlsAlpn" class="input mono" placeholder="h2, http/1.1" />
                </div>
                <div class="field">
                  <label for="tls-cert">证书文件路径</label>
                  <input id="tls-cert" v-model="streamState.tlsCertFile" class="input mono" />
                </div>
                <div class="field">
                  <label for="tls-key">私钥文件路径</label>
                  <input id="tls-key" v-model="streamState.tlsKeyFile" class="input mono" />
                  <p class="field__help">路径必须位于 Agent 受控目录；不接受任意路径注入。</p>
                </div>
              </div>
            </template>

            <template v-if="streamState.security === 'reality'">
              <div class="section-title">REALITY</div>
              <div class="grid grid--form">
                <div class="field">
                  <label for="reality-dest">回落目标</label>
                  <input id="reality-dest" v-model="streamState.realityDest" class="input mono" placeholder="回落目标，如 example.com:443" />
                </div>
                <div class="field">
                  <label for="reality-server-names">允许的域名</label>
                  <input id="reality-server-names" v-model="streamState.realityServerNames" class="input mono" placeholder="逗号分隔" />
                </div>
                <div class="field">
                  <label for="reality-private-key">REALITY 私钥</label>
                  <input id="reality-private-key" v-model="streamState.realityPrivateKey" class="input mono" type="password" autocomplete="off" />
                  <p class="field__help">仅面板与 Agent 使用，不进入成员配置；日志与页面默认脱敏。</p>
                </div>
                <div class="field">
                  <label for="reality-short-ids">短 ID 列表</label>
                  <input id="reality-short-ids" v-model="streamState.realityShortIds" class="input mono" placeholder="逗号分隔，十六进制" />
                </div>
                <div class="field">
                  <label for="reality-public-key">REALITY 公钥</label>
                  <input id="reality-public-key" v-model="streamState.realityPublicKey" class="input mono" />
                </div>
                <div class="field">
                  <label for="reality-fingerprint">指纹</label>
                  <input id="reality-fingerprint" v-model="streamState.realityFingerprint" class="input mono" placeholder="chrome" />
                </div>
                <div class="field">
                  <label for="reality-spider">爬虫路径</label>
                  <input id="reality-spider" v-model="streamState.realitySpiderX" class="input mono" />
                </div>
              </div>
            </template>

            <div class="row">
              <button v-if="streamState.security !== 'tls'" type="button" class="btn btn--sm" @click="clearBlock('tls')">移除 tlsSettings 块</button>
              <button v-if="streamState.security !== 'reality'" type="button" class="btn btn--sm" @click="clearBlock('reality')">移除 realitySettings 块</button>
              <span class="text-xs text-muted">切换 security 不会删除另一分支的配置；需要清理时请显式点击。</span>
            </div>

            <div v-if="preservedStream.length" class="banner banner--info">
              以下 stream_settings 键不在结构化表单中，将原样保留：<span class="mono">{{ preservedStream.join(', ') }}</span>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card__header"><h2>客户端参数（client_settings）</h2></div>
          <div class="card__body stack">
            <p class="text-sm text-muted">面向成员客户端的非秘密参数；服务端私钥一律不写入此处。</p>
            <div class="grid grid--form">
              <div class="field">
                <label for="client-sni">域名</label>
                <input id="client-sni" v-model="clientState.serverName" class="input mono" />
              </div>
              <div class="field">
                <label for="client-public-key">公钥</label>
                <input id="client-public-key" v-model="clientState.publicKey" class="input mono" />
              </div>
              <div class="field">
                <label for="client-short-id">短 ID</label>
                <input id="client-short-id" v-model="clientState.shortId" class="input mono" />
              </div>
              <div class="field">
                <label for="client-fingerprint">指纹</label>
                <input id="client-fingerprint" v-model="clientState.fingerprint" class="input mono" />
              </div>
              <div class="field">
                <label for="client-alpn">协商协议</label>
                <input id="client-alpn" v-model="clientState.alpn" class="input mono" placeholder="h2, http/1.1" />
              </div>
              <div class="field">
                <label for="client-insecure">跳过证书校验</label>
                <select id="client-insecure" v-model="clientState.allowInsecure" class="select">
                  <option value="">不设置</option>
                  <option value="false">false（推荐）</option>
                  <option value="true">true</option>
                </select>
              </div>
            </div>
            <div v-if="preservedClient.length" class="banner banner--info">
              以下 client_settings 键将原样保留：<span class="mono">{{ preservedClient.join(', ') }}</span>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card__header"><h2>服务端密钥</h2></div>
          <div class="card__body stack">
            <p v-if="isEdit" class="text-xs text-muted">
              服务端已保存节点级秘密：<strong>{{ node?.server_key_set ? '是' : '否/未提供' }}</strong>（明文永不返回）
            </p>
            <div class="field">
              <label for="node-server-key">服务端密钥</label>
              <input id="node-server-key" v-model="draft.server_key" class="input mono" type="password" autocomplete="off" />
              <p class="field__help">节点级秘密（如 REALITY 私钥或 SS 主密钥）；不写入成员配置，页面与日志脱敏。</p>
            </div>
            <label v-if="isEdit" class="checkbox">
              <input v-model="keepServerKey" type="checkbox" />
              保持服务端现有密钥不变（不提交 server_key，避免误覆盖）
            </label>
          </div>
        </section>
      </template>

      <template v-else>
        <section class="card">
          <div class="card__header"><h2>原生 JSON 编辑</h2></div>
          <div class="card__body stack">
            <p class="banner banner--info">
              完整提交三个对象：stream_settings / protocol_settings / client_settings。JSON 解析失败时禁止保存，避免静默丢参数。
              clients 由后端注入，保存时会被移除。
            </p>
            <JsonEditor
              :model-value="draft.stream_settings"
              label="stream_settings"
              hint="Xray 原生 streamSettings：network / security / tlsSettings / realitySettings / sockopt 等。"
              @update:model-value="updateStreamJson"
              @update:error="jsonErrors.stream = $event"
            />
            <p v-if="jsonErrors.stream" class="text-xs" style="color: var(--danger)">stream_settings 未通过解析，保存被阻止。</p>
            <JsonEditor
              :model-value="draft.protocol_settings"
              label="protocol_settings"
              hint="入站 settings 中除 clients 外的字段；未知结构必须由后端校验，不能静默丢弃。"
              @update:model-value="updateProtocolJson"
              @update:error="jsonErrors.protocol = $event"
            />
            <p v-if="jsonErrors.protocol" class="text-xs" style="color: var(--danger)">protocol_settings 未通过解析，保存被阻止。</p>
            <JsonEditor
              :model-value="draft.client_settings"
              label="client_settings"
              hint="面向客户端的非秘密参数；不同客户端能力不同，无法表示的组合由服务端明确报错。"
              @update:model-value="updateClientJson"
              @update:error="jsonErrors.client = $event"
            />
            <p v-if="jsonErrors.client" class="text-xs" style="color: var(--danger)">client_settings 未通过解析，保存被阻止。</p>
          </div>
        </section>
      </template>

      <section v-if="validation.errors.length || validation.warnings.length" class="card">
        <div class="card__header"><h2>校验结果</h2></div>
        <div class="card__body stack">
          <div v-if="validation.errors.length" class="banner banner--danger">
            <div>
              <div class="banner__title">阻止保存</div>
              <ul>
                <li v-for="error in validation.errors" :key="error">{{ error }}</li>
              </ul>
            </div>
          </div>
          <div v-if="validation.warnings.length" class="banner banner--warn">
            <div>
              <div class="banner__title">保存前请确认</div>
              <ul>
                <li v-for="warning in validation.warnings" :key="warning">{{ warning }}</li>
              </ul>
            </div>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>