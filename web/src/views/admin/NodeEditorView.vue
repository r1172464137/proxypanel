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
import { unknownKeys, getPath, isRecord, type JsonObject } from '@/lib/json'
import { errorText, fieldErrorsOf } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { NODE_TEMPLATES, TLS_PLACEHOLDER_DOMAIN, newRealityKeyPair, newShortId, type NodeTemplate } from '@/lib/nodeTemplates'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ErrorState from '@/components/ErrorState.vue'
import JsonEditor from '@/components/JsonEditor.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { Loader2 } from 'lucide-vue-next'

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
const showTemplates = ref(false)
// C7：新建时不主动展示原生 JSON 入口，点一下"需要直接改 JSON？"才放出来。
// 编辑已有节点时直接可见（运维常用）。
const advancedAllowed = ref(false)
const appliedTemplate = ref('')
const templateNote = ref('')

/**
 * 后端字段名 → 表单元素 id。
 *
 * 键来自后端 `protocol.Issue.Field` / `WithFieldErrors`（见
 * internal/panel/protocol/protocol.go、internal/panel/api/admin2.go）。
 * 映射不到的字段不会丢：统一在表单顶部的保存错误区按后端字段名列出。
 */
const BACKEND_FIELD_TO_FORM_ID: Record<string, string> = {
  name: 'node-name',
  host: 'node-host',
  port: 'node-port',
  listen: 'node-listen',
  sort_order: 'node-sort',
  protocol: 'node-protocol',
  flow: 'vless-flow',
  cipher: 'ss-method',
  server_key: 'node-server-key',
  'stream_settings.network': 'stream-network',
  'stream_settings.security': 'stream-security',
  'stream_settings.realitySettings': 'reality-dest',
  'stream_settings.realitySettings.dest': 'reality-dest',
  'stream_settings.realitySettings.serverNames': 'reality-server-names',
  'stream_settings.realitySettings.privateKey': 'reality-private-key',
  'stream_settings.realitySettings.shortId': 'reality-short-ids',
  'stream_settings.realitySettings.shortIds': 'reality-short-ids',
  'stream_settings.tlsSettings': 'tls-cert-file',
  'stream_settings.tlsSettings.certificateFile': 'tls-cert-file',
  'stream_settings.tlsSettings.keyFile': 'tls-key',
  'stream_settings.tlsSettings.certificates': 'tls-cert-file',
  'protocol_settings.decryption': 'protocol-decryption',
  'protocol_settings.password': 'ss-password',
  'client_settings.server_name': 'client-sni',
  'client_settings.public_key': 'client-public-key',
  'client_settings.short_id': 'client-short-id',
  'client_settings.fingerprint': 'client-fingerprint',
  'client_settings.alpn': 'client-alpn',
  'client_settings.allow_insecure': 'client-insecure',
}

// 最近一次保存时后端返回的字段级错误（
// details.field_errors）。作为本地校验的补充：同一个字段优先后端消息。
const backendFieldErrors = ref<Record<string, string>>({})

interface BackendFieldErrorItem {
  field: string
  message: string
  formId?: string
}

const backendFieldErrorItems = computed<BackendFieldErrorItem[]>(() =>
  Object.entries(backendFieldErrors.value).map(([field, message]) => ({
    field,
    message,
    formId: BACKEND_FIELD_TO_FORM_ID[field],
  })),
)

const backendErrorsById = computed<Record<string, string>>(() => {
  const map: Record<string, string> = {}
  for (const item of backendFieldErrorItems.value) {
    if (!item.formId) continue
    if (!map[item.formId]) map[item.formId] = item.message
  }
  return map
})

// 套用模板：直接覆盖协议/传输/密钥相关的字段，保留名称与服务器选择。
// REALITY 的密钥对需要 WebCrypto，异步生成；其他模板同步完成。
// 生成一对 REALITY 密钥（私钥 + 公钥一起填），省掉用户去命令行跑 xray x25519。
async function generateRealityKeys(): Promise<void> {
  try {
    const pair = await newRealityKeyPair()
    streamState.realityPrivateKey = pair.privateKey
    streamState.realityPublicKey = pair.publicKey
    if (!streamState.realityFingerprint) streamState.realityFingerprint = 'chrome'
    // 成员配置读的是 client_settings.publicKey；只填服务端一侧会让成员拿到空公钥。
    clientState.publicKey = pair.publicKey
    templateNote.value = '已生成 REALITY 密钥对。'
    draft.stream_settings = applyStreamView(draft.stream_settings, streamState)
    draft.client_settings = applyClientView(draft.client_settings, clientState)
  } catch (err) {
    saveError.value = `密钥生成失败：${err instanceof Error ? err.message : String(err)}`
  }
}

async function applyTemplate(tpl: NodeTemplate): Promise<void> {
  const patch = tpl.build(draft.name || '')
  appliedTemplate.value = tpl.id
  templateNote.value = ''
  // 换模板是一次新的尝试：旧的字段级错误不再适用于新的表单内容。
  backendFieldErrors.value = {}

  if (patch.protocol) draft.protocol = patch.protocol
  if (patch.port) draft.port = patch.port
  if (patch.flow !== undefined) draft.flow = patch.flow
  if (patch.cipher !== undefined) draft.cipher = patch.cipher
  if (patch.server_key !== undefined) draft.server_key = patch.server_key
  if (patch.stream_settings) draft.stream_settings = patch.stream_settings as NodeDraft['stream_settings']
  if (patch.protocol_settings) draft.protocol_settings = patch.protocol_settings as NodeDraft['protocol_settings']
  if (patch.client_settings) draft.client_settings = patch.client_settings as NodeDraft['client_settings']

  // REALITY 模板要生成真实的密钥对，否则保存后节点起不来。
  if (tpl.id === 'vless-reality') {
    try {
      const pair = await newRealityKeyPair()
      const realitySettings = ((draft.stream_settings as Record<string, unknown>).realitySettings ?? {}) as Record<string, unknown>
      draft.stream_settings = {
        ...(draft.stream_settings as Record<string, unknown>),
        realitySettings: {
          ...realitySettings,
          privateKey: pair.privateKey,
          settings: { publicKey: pair.publicKey, fingerprint: 'chrome', spiderX: '/' },
        },
      }
      // 公钥必须与刚生成的私钥配对：模板只带 shortId/serverName，
      // 公钥只能异步补进 client_settings（成员配置从这里取）。
      draft.client_settings = {
        ...(draft.client_settings as JsonObject),
        publicKey: pair.publicKey,
      }
      const shortId = Array.isArray(realitySettings.shortIds) ? String(realitySettings.shortIds[0] ?? '') : ''
      templateNote.value = `已生成 REALITY 密钥对和 shortId${shortId ? `（${shortId}）` : ''}。下一步：把「回落目标」和「允许的域名」改成你想伪装的站点，就可以直接保存。`
    } catch (err) {
      templateNote.value = `REALITY 密钥生成失败（${err instanceof Error ? err.message : String(err)}）。请点「生成一对」或手动填写私钥与公钥，否则保存会被拒绝。`
    }
  } else if (tpl.id === 'ss2022') {
    templateNote.value = '已生成服务端密钥与密码。保存后即可使用，不需要域名或证书。'
  } else {
    templateNote.value =
      `已填好协议、传输和占位域名 ${TLS_PLACEHOLDER_DOMAIN}（「证书域名」和「客户端参数 → 域名」）。下一步：把这两处改成你自己的域名，并填「证书文件路径」/「私钥文件路径」；没有证书无法保存。`
  }

  // 注意方向：模板写的是 draft，这里必须把 draft 反向灌进各 view。
  // 不能调 syncDraft()——那是"用户编辑表单 -> 写 draft"的方向，会用尚未刷新
  // 的旧 protocolState 把模板刚写入的密钥覆盖成空值（实测 password 会变空）。
  Object.assign(streamState, streamView(draft.stream_settings))
  Object.assign(protocolState, protocolView(draft.protocol_settings, draft.protocol))
  Object.assign(clientState, clientView(draft.client_settings))
}


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

// 本地校验看不见的硬阻塞：TLS 节点没有证书材料时，面板会接受保存但核心无法
// 启动（下发到 Agent 才失败）。模板只能给占位域名，所以这里把“证书未配置”
// 明确成阻断错误，而不是只给一条容易被忽略的警告。
// 已内联证书（tlsSettings.certificates[0].certificate/key）或已填文件路径的节点不受影响。
const TLS_CERT_MISSING_MESSAGE =
  `TLS 证书文件未配置：请填写「证书文件路径」和「私钥文件路径」（Agent 受控目录下的真实文件），并把证书域名从占位值 ${TLS_PLACEHOLDER_DOMAIN} 改掉。`

const tlsCertMissing = computed(() => {
  if (streamState.security !== 'tls') return false
  const tlsSettings = getPath(previewDraft.value.stream_settings, 'tlsSettings')
  if (!isRecord(tlsSettings)) return true
  if (String(tlsSettings.certificateFile ?? '').trim() || String(tlsSettings.keyFile ?? '').trim()) return false
  const certificates = tlsSettings.certificates
  if (Array.isArray(certificates) && certificates.length > 0) return false
  return true
})

const extraErrorItems = computed<{ field?: string; message: string }[]>(() =>
  tlsCertMissing.value ? [{ field: 'tls-cert-file', message: TLS_CERT_MISSING_MESSAGE }] : [],
)

const allErrorItems = computed(() => [...validation.value.errorItems, ...extraErrorItems.value])
const allErrors = computed(() => allErrorItems.value.map((item) => item.message))
// 证书缺失已经升级为阻断错误，不再重复保留同一条警告。
const allWarnings = computed(() =>
  validation.value.warningItems
    .filter((item) => !(tlsCertMissing.value && item.field === 'tls-cert-file'))
    .map((item) => item.message),
)

// 字段级错误：把校验结果按表单元素 id 分组，让错误直接显示在对应输入框下面，
// 而不是只在页面底部汇总一次（用户得自己去找是哪一项）。
// 后端返回的字段级错误优先于本地推断（服务端才是权威）。
const fieldErrors = computed(() => {
  const map: Record<string, string> = {}
  for (const item of allErrorItems.value) {
    if (!item.field) continue
    // 同一个字段只显示第一条，避免叠一堆
    if (!map[item.field]) map[item.field] = item.message
  }
  return map
})

function errorFor(field: string): string {
  return backendErrorsById.value[field] ?? fieldErrors.value[field] ?? ''
}
const showFallbacks = computed(() => draft.protocol === 'vless' || draft.protocol === 'trojan')
const security = computed(() => String(streamState.security || 'none'))

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
  // 每次提交都从干净状态开始：旧的字段级错误不对应当前这次请求。
  backendFieldErrors.value = {}
  if (jsonErrors.stream || jsonErrors.protocol || jsonErrors.client) {
    saveError.value = '高级 JSON 存在解析错误，已阻止提交以免丢失参数'
    tab.value = 'advanced'
    return
  }
  syncDraft()
  const result = buildNodeWrite(draft, { omitServerKey: isEdit.value && keepServerKey.value })
  if (!result.payload || extraErrorItems.value.length) {
    saveError.value = [...result.errors, ...extraErrorItems.value.map((item) => item.message)].join('；')
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
    // 后端会指出具体是哪个字段（details.field_errors），映射到对应输入框下方；
    // 映射不到的也会在顶部错误区按后端字段名列出。
    backendFieldErrors.value = fieldErrorsOf(caught)
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
  <div class="space-y-6">
    <PageHeader
      :title="isEdit ? '编辑节点' : '新建节点'"
      description="VLESS / VMess / Trojan / Shadowsocks（含 2022）。结构化表单与原生 JSON 编辑同一份数据：未知字段保留并在保存时原样提交。"
    >
      <Button as-child variant="outline">
        <router-link to="/admin/nodes">返回列表</router-link>
      </Button>
      <Button :disabled="saving" @click="save">
        <Loader2 v-if="saving" class="animate-spin" aria-hidden="true" />
        {{ saving ? '保存中…' : '保存节点' }}
      </Button>
    </PageHeader>

    <LoadingBlock v-if="loading" label="正在读取节点" />
    <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />
    <div v-else class="space-y-4">
      <Alert v-if="saveError" variant="destructive">
        <AlertTitle>保存失败</AlertTitle>
        <AlertDescription>
          <p class="whitespace-pre-wrap break-all">{{ saveError }}</p>
          <!-- 后端字段级错误：能映射到输入框的已在下面就地标注，映射不到的（或
               表单当前没展示的字段）在这里按后端字段名全部列出，不丢信息。 -->
          <div v-if="backendFieldErrorItems.length" class="mt-2 space-y-1">
            <p class="text-xs font-medium">服务端指出了这些字段：</p>
            <ul class="list-disc space-y-1 pl-4 text-xs">
              <li v-for="item in backendFieldErrorItems" :key="item.field">
                <span class="font-mono">{{ item.field }}</span>：{{ item.message }}
                <span v-if="item.formId" class="text-muted-foreground">（对应表单控件 #{{ item.formId }}）</span>
                <span v-else class="text-muted-foreground">（表单没有对应控件）</span>
              </li>
            </ul>
          </div>
        </AlertDescription>
      </Alert>
      <ErrorState v-if="serversError" :error="serversError" @retry="load" show-retry />
      <Alert v-if="!servers.length">
        <AlertTitle>还没有服务器档案</AlertTitle>
        <AlertDescription>
          必须先创建服务器才能保存节点。
          <router-link to="/admin/servers" class="font-medium underline underline-offset-4">前往服务器</router-link>
        </AlertDescription>
      </Alert>
      <Alert v-if="node">
        <AlertDescription>
          节点 ID <span class="font-mono">{{ node.id }}</span> · 版本 {{ node.version }} · 更新于 {{ formatDateTime(node.updated_at) }}
        </AlertDescription>
      </Alert>

      <!-- 新建节点时先选模板：一套可用配置一键填好，用户只需要改名称与端口。
           编辑已有节点时不显示，避免覆盖现有配置。 -->
      <Card v-if="!isEdit">
        <CardHeader class="flex-row flex-wrap items-center justify-between gap-3 space-y-0">
          <CardTitle class="text-base">用模板快速创建</CardTitle>
          <Button variant="outline" size="sm" @click="showTemplates = !showTemplates">
            {{ showTemplates ? '收起' : '展开' }}
          </Button>
        </CardHeader>
        <CardContent v-if="showTemplates" class="space-y-4">
          <p class="text-sm text-muted-foreground">
            选一个模板会自动填好协议、传输、加密层和需要的密钥，你只需要改名称和端口。
            也可以跳过，直接在下面手动配置。
          </p>
          <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            <button
              v-for="tpl in NODE_TEMPLATES"
              :key="tpl.id"
              type="button"
              class="rounded-lg border border-border bg-background p-3 text-left transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              :class="appliedTemplate === tpl.id ? 'border-primary bg-accent ring-1 ring-primary' : ''"
              :aria-pressed="appliedTemplate === tpl.id"
              @click="applyTemplate(tpl)"
            >
              <span class="block text-sm font-medium">{{ tpl.label }}</span>
              <span class="mt-1 block text-xs text-muted-foreground">{{ tpl.description }}</span>
              <span class="mt-2 block text-xs text-muted-foreground">需要准备：{{ tpl.needs }}</span>
            </button>
          </div>
          <p v-if="templateNote" role="status" class="rounded-md border border-border bg-muted px-3 py-2 text-sm">
            {{ templateNote }}
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">基本信息</CardTitle>
        </CardHeader>
        <CardContent class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label for="node-name">名称</Label>
            <Input
              id="node-name"
              v-model="draft.name"
              required
              :aria-invalid="Boolean(errorFor('node-name'))"
              :class="errorFor('node-name') ? 'border-destructive focus-visible:ring-destructive' : ''"
            />
            <p v-if="errorFor('node-name')" class="text-xs text-destructive">{{ errorFor('node-name') }}</p>
          </div>
          <div class="space-y-1.5">
            <Label for="node-server">所属服务器</Label>
            <select
              id="node-server"
              v-model="draft.server_id"
              :aria-invalid="Boolean(errorFor('node-server'))"
              :class="[
                'flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
                errorFor('node-server') ? 'border-destructive' : '',
              ]"
            >
              <option value="">请选择</option>
              <option v-for="option in serverOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
            <p v-if="errorFor('node-server')" class="text-xs text-destructive">{{ errorFor('node-server') }}</p>
          </div>
          <div class="space-y-1.5">
            <Label for="node-protocol">协议</Label>
            <select
              id="node-protocol"
              v-model="draft.protocol"
              :aria-invalid="Boolean(errorFor('node-protocol'))"
              :class="[
                'flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
                errorFor('node-protocol') ? 'border-destructive' : '',
              ]"
            >
              <option v-for="(label, value) in NODE_PROTOCOL_LABELS" :key="value" :value="value">{{ label }}</option>
            </select>
            <p v-if="errorFor('node-protocol')" class="text-xs text-destructive">{{ errorFor('node-protocol') }}</p>
          </div>
          <div class="space-y-1.5">
            <Label for="node-host">对外地址</Label>
            <Input
              id="node-host"
              v-model="draft.host"
              class="font-mono"
              placeholder="节点域名或 IP（写入成员配置）"
              :aria-invalid="Boolean(errorFor('node-host'))"
              :class="errorFor('node-host') ? 'border-destructive focus-visible:ring-destructive' : ''"
            />
            <p v-if="errorFor('node-host')" class="text-xs text-destructive">{{ errorFor('node-host') }}</p>
          </div>
          <div class="space-y-1.5">
            <Label for="node-port">监听端口</Label>
            <Input
              id="node-port"
              v-model="draft.port"
              inputmode="numeric"
              :aria-invalid="Boolean(errorFor('node-port'))"
              :class="errorFor('node-port') ? 'border-destructive focus-visible:ring-destructive' : ''"
            />
            <p v-if="errorFor('node-port')" class="text-xs text-destructive">{{ errorFor('node-port') }}</p>
            <p class="text-xs text-muted-foreground">节点在本机实际监听的端口。</p>
          </div>
          <div class="space-y-1.5">
            <Label for="node-external-port">对外端口（可选）</Label>
            <Input
              id="node-external-port"
              v-model="draft.external_port"
              inputmode="numeric"
              placeholder="留空表示与监听端口相同"
              :aria-invalid="Boolean(errorFor('external_port'))"
              :class="errorFor('external_port') ? 'border-destructive focus-visible:ring-destructive' : ''"
            />
            <p v-if="errorFor('external_port')" class="text-xs text-destructive">{{ errorFor('external_port') }}</p>
            <p class="text-xs text-muted-foreground">
              成员客户端实际连接的端口。用于「对外 443 → 本机实际 20101」这类中转：
              客户端连「对外地址 + 这里填的端口」，本机仍监听上面的监听端口。
              端口转发需要你在中转机上自行配置（nginx stream / iptables 等），面板不代管。
            </p>
          </div>
          <div class="space-y-1.5">
            <Label for="node-listen">监听地址</Label>
            <Input
              id="node-listen"
              v-model="draft.listen"
              class="font-mono"
              placeholder="0.0.0.0"
              :aria-invalid="Boolean(errorFor('node-listen'))"
              :class="errorFor('node-listen') ? 'border-destructive focus-visible:ring-destructive' : ''"
            />
            <p v-if="errorFor('node-listen')" class="text-xs text-destructive">{{ errorFor('node-listen') }}</p>
          </div>
          <div class="space-y-1.5">
            <Label for="node-sort">排序</Label>
            <Input
              id="node-sort"
              v-model="draft.sort_order"
              inputmode="numeric"
              :aria-invalid="Boolean(errorFor('node-sort'))"
              :class="errorFor('node-sort') ? 'border-destructive focus-visible:ring-destructive' : ''"
            />
            <p v-if="errorFor('node-sort')" class="text-xs text-destructive">{{ errorFor('node-sort') }}</p>
          </div>
          <div class="space-y-1.5 self-end">
            <div class="flex items-center gap-2">
              <Checkbox
                id="node-enabled"
                :model-value="draft.enabled"
                @update:model-value="(value) => (draft.enabled = value === true)"
              />
              <Label for="node-enabled" class="font-normal">启用该节点</Label>
            </div>
            <p class="text-xs text-muted-foreground">
              关掉后这条线路停止提供服务，正在使用的连接会断开。重新开启并保存后会重新上线。
            </p>
          </div>
        </CardContent>
      </Card>

      <div class="flex flex-wrap items-center gap-3">
        <Tabs
          :model-value="tab"
          @update:model-value="(value) => (tab = value === 'advanced' ? 'advanced' : 'structured')"
        >
          <TabsList>
            <TabsTrigger value="structured">结构化参数</TabsTrigger>
            <TabsTrigger v-if="isEdit || advancedAllowed" value="advanced">原生 JSON（高级）</TabsTrigger>
          </TabsList>
        </Tabs>
        <template v-if="isEdit || advancedAllowed">
          <span class="text-xs text-muted-foreground">两种方式编辑同一对象；JSON 中的额外字段不会丢失。</span>
        </template>
        <template v-else>
          <Button variant="outline" size="sm" @click="advancedAllowed = true">需要直接改 JSON？</Button>
          <span class="text-xs text-muted-foreground">
            新建时默认用结构化表单，字段说明更清楚。熟悉 Xray 原生配置可以切到 JSON 模式。
          </span>
        </template>
      </div>

      <template v-if="tab === 'structured'">
        <Card>
          <CardHeader>
            <CardTitle class="text-base">协议参数（protocol_settings）</CardTitle>
          </CardHeader>
          <CardContent class="space-y-4">
            <Alert v-if="hasClientsInSettings">
              <AlertDescription>
                protocol_settings 中存在 clients：客户端凭据只能由后端注入，保存时会移除该字段（其它字段保留）。
              </AlertDescription>
            </Alert>

            <div class="grid gap-4 sm:grid-cols-2">
              <template v-if="draft.protocol === 'vless'">
                <div class="space-y-1.5">
                  <Label for="protocol-decryption">加密方式</Label>
                  <Input
                    id="protocol-decryption"
                    v-model="protocolState.decryption"
                    class="font-mono"
                    placeholder="none"
                    :aria-invalid="Boolean(errorFor('protocol-decryption'))"
                    :class="errorFor('protocol-decryption') ? 'border-destructive focus-visible:ring-destructive' : ''"
                  />
                  <p v-if="errorFor('protocol-decryption')" class="text-xs text-destructive">{{ errorFor('protocol-decryption') }}</p>
                  <p class="text-xs text-muted-foreground">VLESS 常用 none</p>
                </div>
                <div class="space-y-1.5">
                  <Label for="vless-flow">流控</Label>
                  <select
                    id="vless-flow"
                    v-model="draft.flow"
                    class="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                  >
                    <option value="">不使用</option>
                    <option value="xtls-rprx-vision">xtls-rprx-vision</option>
                  </select>
                </div>
              </template>

              <template v-if="draft.protocol === 'vmess'">
                <div class="space-y-1.5">
                  <Label for="vmess-insecure">禁用不安全加密</Label>
                  <select
                    id="vmess-insecure"
                    v-model="protocolState.vmessDisableInsecure"
                    class="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                  >
                    <option value="">不设置（沿用核心默认）</option>
                    <option value="true">true</option>
                    <option value="false">false</option>
                  </select>
                </div>
              </template>

              <template v-if="draft.protocol === 'shadowsocks'">
                <div class="space-y-1.5">
                  <Label for="ss-method">加密算法</Label>
                  <Input
                    id="ss-method"
                    v-model="cipherValue"
                    class="font-mono"
                    list="ss-methods"
                    placeholder="aes-256-gcm 或 2022-blake3-*"
                    :aria-invalid="Boolean(errorFor('ss-method'))"
                    :class="errorFor('ss-method') ? 'border-destructive focus-visible:ring-destructive' : ''"
                  />
                  <datalist id="ss-methods">
                    <option v-for="method in SS_METHOD_OPTIONS" :key="method" :value="method" />
                  </datalist>
                  <p v-if="errorFor('ss-method')" class="text-xs text-destructive">{{ errorFor('ss-method') }}</p>
                  <p class="text-xs text-muted-foreground">2022 系列方法需配套多用户主密钥；被核心移除的算法不要声称支持。</p>
                </div>
                <div class="space-y-1.5">
                  <Label for="ss-password">服务端密钥</Label>
                  <Input
                    id="ss-password"
                    v-model="protocolState.ssPassword"
                    class="font-mono"
                    type="password"
                    autocomplete="off"
                    :aria-invalid="Boolean(errorFor('ss-password'))"
                    :class="errorFor('ss-password') ? 'border-destructive focus-visible:ring-destructive' : ''"
                  />
                  <p v-if="errorFor('ss-password')" class="text-xs text-destructive">{{ errorFor('ss-password') }}</p>
                  <p class="text-xs text-muted-foreground">只保存在面板；不写入成员配置。留空表示由后端注入。</p>
                </div>
                <div class="space-y-1.5">
                  <Label for="ss-network">传输网络</Label>
                  <select
                    id="ss-network"
                    v-model="protocolState.ssNetwork"
                    class="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                  >
                    <option value="">不设置</option>
                    <option value="tcp">tcp</option>
                    <option value="udp">udp</option>
                    <option value="both">both</option>
                  </select>
                </div>
              </template>
            </div>

            <div v-if="showFallbacks" class="space-y-1.5">
              <Label for="protocol-fallbacks">回落规则（高级）</Label>
              <Textarea
                id="protocol-fallbacks"
                v-model="protocolState.fallbacksText"
                class="min-h-[120px] font-mono"
                spellcheck="false"
                placeholder='[{"dest": 80, "alpn": ""}]'
                :aria-invalid="Boolean(errorFor('protocol-fallbacks'))"
                :class="errorFor('protocol-fallbacks') ? 'border-destructive focus-visible:ring-destructive' : ''"
              />
              <p v-if="errorFor('protocol-fallbacks')" class="text-xs text-destructive">{{ errorFor('protocol-fallbacks') }}</p>
              <p class="text-xs text-muted-foreground">与 Xray 原生 fallbacks 一致；留空表示不设置。非数组会在保存校验中被阻止。</p>
            </div>

            <p v-if="preservedProtocol.length" class="rounded-md border border-border bg-muted px-3 py-2 text-xs text-muted-foreground">
              以下 protocol_settings 键不在结构化表单中，将原样保留：<span class="font-mono">{{ preservedProtocol.join(', ') }}</span>
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="flex-row flex-wrap items-center justify-between gap-2 space-y-0">
            <CardTitle class="text-base">传输与安全（stream_settings）</CardTitle>
            <StatusBadge :status="security" :label="`security: ${security}`" />
          </CardHeader>
          <CardContent class="space-y-4">
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="space-y-1.5">
                <Label for="stream-network">传输方式</Label>
                <Input
                  id="stream-network"
                  v-model="streamState.network"
                  class="font-mono"
                  list="networks"
                  :aria-invalid="Boolean(errorFor('stream-network'))"
                  :class="errorFor('stream-network') ? 'border-destructive focus-visible:ring-destructive' : ''"
                />
                <datalist id="networks">
                  <option v-for="network in NETWORK_OPTIONS" :key="network" :value="network" />
                </datalist>
                <p v-if="errorFor('stream-network')" class="text-xs text-destructive">{{ errorFor('stream-network') }}</p>
                <p class="text-xs text-muted-foreground">常用 tcp / ws / grpc / httpupgrade；核心已移除的传输请勿声称支持。</p>
              </div>
              <div class="space-y-1.5">
                <Label for="stream-security">加密层</Label>
                <select
                  id="stream-security"
                  v-model="streamState.security"
                  :aria-invalid="Boolean(errorFor('stream-security'))"
                  :class="[
                    'flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
                    errorFor('stream-security') ? 'border-destructive' : '',
                  ]"
                >
                  <option v-for="option in SECURITY_OPTIONS" :key="option" :value="option">{{ option }}</option>
                </select>
                <p v-if="errorFor('stream-security')" class="text-xs text-destructive">{{ errorFor('stream-security') }}</p>
              </div>
            </div>

            <template v-if="streamState.security === 'tls'">
              <div class="flex items-center gap-3">
                <span class="text-sm font-medium">TLS</span>
                <Separator class="flex-1" />
              </div>
              <div class="grid gap-4 sm:grid-cols-2">
                <div class="space-y-1.5">
                  <Label for="tls-sni">证书域名</Label>
                  <Input id="tls-sni" v-model="streamState.tlsServerName" class="font-mono" />
                </div>
                <div class="space-y-1.5">
                  <Label for="tls-alpn">协商协议</Label>
                  <Input id="tls-alpn" v-model="streamState.tlsAlpn" class="font-mono" placeholder="h2, http/1.1" />
                </div>
                <div class="space-y-1.5">
                  <Label for="tls-cert-file">证书文件路径</Label>
                  <Input
                    id="tls-cert-file"
                    v-model="streamState.tlsCertFile"
                    class="font-mono"
                    :aria-invalid="Boolean(errorFor('tls-cert-file'))"
                    :class="errorFor('tls-cert-file') ? 'border-destructive focus-visible:ring-destructive' : ''"
                  />
                  <p v-if="errorFor('tls-cert-file')" class="text-xs text-destructive">{{ errorFor('tls-cert-file') }}</p>
                </div>
                <div class="space-y-1.5">
                  <Label for="tls-key">私钥文件路径</Label>
                  <Input
                    id="tls-key"
                    v-model="streamState.tlsKeyFile"
                    class="font-mono"
                    :aria-invalid="Boolean(errorFor('tls-key'))"
                    :class="errorFor('tls-key') ? 'border-destructive focus-visible:ring-destructive' : ''"
                  />
                  <p v-if="errorFor('tls-key')" class="text-xs text-destructive">{{ errorFor('tls-key') }}</p>
                  <p class="text-xs text-muted-foreground">路径必须位于 Agent 受控目录；不接受任意路径注入。</p>
                </div>
              </div>
            </template>

            <template v-if="streamState.security === 'reality'">
              <div class="flex items-center gap-3">
                <span class="text-sm font-medium">REALITY</span>
                <Separator class="flex-1" />
              </div>
              <div class="grid gap-4 sm:grid-cols-2">
                <div class="space-y-1.5">
                  <Label for="reality-dest">回落目标</Label>
                  <Input
                    id="reality-dest"
                    v-model="streamState.realityDest"
                    class="font-mono"
                    placeholder="回落目标，如 example.com:443"
                    :aria-invalid="Boolean(errorFor('reality-dest'))"
                    :class="errorFor('reality-dest') ? 'border-destructive focus-visible:ring-destructive' : ''"
                  />
                  <p v-if="errorFor('reality-dest')" class="text-xs text-destructive">{{ errorFor('reality-dest') }}</p>
                </div>
                <div class="space-y-1.5">
                  <Label for="reality-server-names">允许的域名</Label>
                  <Input
                    id="reality-server-names"
                    v-model="streamState.realityServerNames"
                    class="font-mono"
                    placeholder="逗号分隔"
                    :aria-invalid="Boolean(errorFor('reality-server-names'))"
                    :class="errorFor('reality-server-names') ? 'border-destructive focus-visible:ring-destructive' : ''"
                  />
                  <p v-if="errorFor('reality-server-names')" class="text-xs text-destructive">{{ errorFor('reality-server-names') }}</p>
                </div>
                <div class="space-y-1.5 sm:col-span-2">
                  <Label for="reality-private-key">REALITY 私钥</Label>
                  <div class="flex items-start gap-2">
                    <div class="flex-1 space-y-1.5">
                      <Input
                        id="reality-private-key"
                        v-model="streamState.realityPrivateKey"
                        class="font-mono"
                        type="password"
                        autocomplete="off"
                        :aria-invalid="Boolean(errorFor('reality-private-key'))"
                        :class="errorFor('reality-private-key') ? 'border-destructive focus-visible:ring-destructive' : ''"
                      />
                      <p v-if="errorFor('reality-private-key')" class="text-xs text-destructive">{{ errorFor('reality-private-key') }}</p>
                    </div>
                    <Button type="button" variant="outline" size="sm" @click="generateRealityKeys">生成一对</Button>
                  </div>
                  <p class="text-xs text-muted-foreground">
                    只有面板和节点自己知道，成员看不到。点「生成一对」会自动填好私钥和公钥。
                  </p>
                </div>
                <div class="space-y-1.5 sm:col-span-2">
                  <Label for="reality-short-ids">短 ID 列表</Label>
                  <div class="flex items-start gap-2">
                    <div class="flex-1 space-y-1.5">
                      <Input
                        id="reality-short-ids"
                        v-model="streamState.realityShortIds"
                        class="font-mono"
                        placeholder="逗号分隔，十六进制"
                        :aria-invalid="Boolean(errorFor('reality-short-ids'))"
                        :class="errorFor('reality-short-ids') ? 'border-destructive focus-visible:ring-destructive' : ''"
                      />
                      <p v-if="errorFor('reality-short-ids')" class="text-xs text-destructive">{{ errorFor('reality-short-ids') }}</p>
                    </div>
                    <Button type="button" variant="outline" size="sm" @click="streamState.realityShortIds = newShortId(8)">生成</Button>
                  </div>
                  <p class="text-xs text-muted-foreground">一串十六进制，用来区分同时使用同一域名的不同节点。留空也可以工作。</p>
                </div>
                <div class="space-y-1.5">
                  <Label for="reality-public-key">REALITY 公钥</Label>
                  <Input id="reality-public-key" v-model="streamState.realityPublicKey" class="font-mono" />
                  <p class="text-xs text-muted-foreground">由私钥推出，会写进成员的客户端配置。点上面的「生成一对」会一起填好。</p>
                </div>
                <div class="space-y-1.5">
                  <Label for="reality-fingerprint">指纹</Label>
                  <Input id="reality-fingerprint" v-model="streamState.realityFingerprint" class="font-mono" placeholder="chrome" />
                </div>
                <div class="space-y-1.5">
                  <Label for="reality-spider">爬虫路径</Label>
                  <Input id="reality-spider" v-model="streamState.realitySpiderX" class="font-mono" />
                </div>
              </div>
            </template>

            <div class="flex flex-wrap items-center gap-2">
              <Button v-if="streamState.security !== 'tls'" type="button" variant="outline" size="sm" @click="clearBlock('tls')">
                移除 tlsSettings 块
              </Button>
              <Button v-if="streamState.security !== 'reality'" type="button" variant="outline" size="sm" @click="clearBlock('reality')">
                移除 realitySettings 块
              </Button>
              <span class="text-xs text-muted-foreground">切换 security 不会删除另一分支的配置；需要清理时请显式点击。</span>
            </div>

            <p v-if="preservedStream.length" class="rounded-md border border-border bg-muted px-3 py-2 text-xs text-muted-foreground">
              以下 stream_settings 键不在结构化表单中，将原样保留：<span class="font-mono">{{ preservedStream.join(', ') }}</span>
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="text-base">客户端参数（client_settings）</CardTitle>
          </CardHeader>
          <CardContent class="space-y-4">
            <p class="text-sm text-muted-foreground">面向成员客户端的非秘密参数；服务端私钥一律不写入此处。</p>
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="space-y-1.5">
                <Label for="client-sni">域名</Label>
                <Input
                  id="client-sni"
                  v-model="clientState.serverName"
                  class="font-mono"
                  :aria-invalid="Boolean(errorFor('client-sni'))"
                  :class="errorFor('client-sni') ? 'border-destructive focus-visible:ring-destructive' : ''"
                />
                <p v-if="errorFor('client-sni')" class="text-xs text-destructive">{{ errorFor('client-sni') }}</p>
                <p class="text-xs text-muted-foreground">TLS / REALITY 必须填：成员客户端用这个域名握手（REALITY 通常与「允许的域名」一致）。</p>
              </div>
              <div class="space-y-1.5">
                <Label for="client-public-key">公钥</Label>
                <Input
                  id="client-public-key"
                  v-model="clientState.publicKey"
                  class="font-mono"
                  :aria-invalid="Boolean(errorFor('client-public-key'))"
                  :class="errorFor('client-public-key') ? 'border-destructive focus-visible:ring-destructive' : ''"
                />
                <p v-if="errorFor('client-public-key')" class="text-xs text-destructive">{{ errorFor('client-public-key') }}</p>
              </div>
              <div class="space-y-1.5">
                <Label for="client-short-id">短 ID</Label>
                <Input
                  id="client-short-id"
                  v-model="clientState.shortId"
                  class="font-mono"
                  :aria-invalid="Boolean(errorFor('client-short-id'))"
                  :class="errorFor('client-short-id') ? 'border-destructive focus-visible:ring-destructive' : ''"
                />
                <p v-if="errorFor('client-short-id')" class="text-xs text-destructive">{{ errorFor('client-short-id') }}</p>
              </div>
              <div class="space-y-1.5">
                <Label for="client-fingerprint">指纹</Label>
                <Input id="client-fingerprint" v-model="clientState.fingerprint" class="font-mono" />
              </div>
              <div class="space-y-1.5">
                <Label for="client-alpn">协商协议</Label>
                <Input id="client-alpn" v-model="clientState.alpn" class="font-mono" placeholder="h2, http/1.1" />
              </div>
              <div class="space-y-1.5">
                <Label for="client-insecure">跳过证书校验</Label>
                <select
                  id="client-insecure"
                  v-model="clientState.allowInsecure"
                  class="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                >
                  <option value="">不设置</option>
                  <option value="false">false（推荐）</option>
                  <option value="true">true</option>
                </select>
              </div>
            </div>
            <p v-if="preservedClient.length" class="rounded-md border border-border bg-muted px-3 py-2 text-xs text-muted-foreground">
              以下 client_settings 键将原样保留：<span class="font-mono">{{ preservedClient.join(', ') }}</span>
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="text-base">服务端密钥</CardTitle>
          </CardHeader>
          <CardContent class="space-y-4">
            <p v-if="isEdit" class="text-xs text-muted-foreground">
              服务端已保存节点级秘密：<strong class="text-foreground">{{ node?.server_key_set ? '是' : '否/未提供' }}</strong>（明文永不返回）
            </p>
            <div class="space-y-1.5">
              <Label for="node-server-key">服务端密钥</Label>
              <Input id="node-server-key" v-model="draft.server_key" class="font-mono" type="password" autocomplete="off" />
              <p class="text-xs text-muted-foreground">节点级秘密（如 REALITY 私钥或 SS 主密钥）；不写入成员配置，页面与日志脱敏。</p>
            </div>
            <div v-if="isEdit" class="flex items-start gap-2">
              <Checkbox
                id="keep-server-key"
                class="mt-0.5"
                :model-value="keepServerKey"
                @update:model-value="(value) => (keepServerKey = value === true)"
              />
              <Label for="keep-server-key" class="font-normal leading-snug">
                保持服务端现有密钥不变（不提交 server_key，避免误覆盖）
              </Label>
            </div>
          </CardContent>
        </Card>
      </template>

      <template v-else>
        <Card>
          <CardHeader>
            <CardTitle class="text-base">原生 JSON 编辑</CardTitle>
          </CardHeader>
          <CardContent class="space-y-4">
            <Alert>
              <AlertDescription>
                完整提交三个对象：stream_settings / protocol_settings / client_settings。JSON 解析失败时禁止保存，避免静默丢参数。
                clients 由后端注入，保存时会被移除。
              </AlertDescription>
            </Alert>
            <div class="space-y-1.5">
              <JsonEditor
                :model-value="draft.stream_settings"
                label="stream_settings"
                hint="Xray 原生 streamSettings：network / security / tlsSettings / realitySettings / sockopt 等。"
                @update:model-value="updateStreamJson"
                @update:error="jsonErrors.stream = $event"
              />
              <p v-if="jsonErrors.stream" class="text-xs text-destructive">stream_settings 未通过解析，保存被阻止。</p>
            </div>
            <div class="space-y-1.5">
              <JsonEditor
                :model-value="draft.protocol_settings"
                label="protocol_settings"
                hint="入站 settings 中除 clients 外的字段；未知结构必须由后端校验，不能静默丢弃。"
                @update:model-value="updateProtocolJson"
                @update:error="jsonErrors.protocol = $event"
              />
              <p v-if="jsonErrors.protocol" class="text-xs text-destructive">protocol_settings 未通过解析，保存被阻止。</p>
            </div>
            <div class="space-y-1.5">
              <JsonEditor
                :model-value="draft.client_settings"
                label="client_settings"
                hint="面向客户端的非秘密参数；不同客户端能力不同，无法表示的组合由服务端明确报错。"
                @update:model-value="updateClientJson"
                @update:error="jsonErrors.client = $event"
              />
              <p v-if="jsonErrors.client" class="text-xs text-destructive">client_settings 未通过解析，保存被阻止。</p>
            </div>
          </CardContent>
        </Card>
      </template>

      <Card v-if="allErrors.length || allWarnings.length">
        <CardHeader>
          <CardTitle class="text-base">校验结果</CardTitle>
        </CardHeader>
        <CardContent class="space-y-3">
          <Alert v-if="allErrors.length" variant="destructive">
            <AlertTitle>阻止保存</AlertTitle>
            <AlertDescription>
              <ul class="list-disc space-y-1 pl-4">
                <li v-for="error in allErrors" :key="error">{{ error }}</li>
              </ul>
            </AlertDescription>
          </Alert>
          <Alert v-if="allWarnings.length">
            <AlertTitle>保存前请确认</AlertTitle>
            <AlertDescription>
              <ul class="list-disc space-y-1 pl-4">
                <li v-for="warning in allWarnings" :key="warning">{{ warning }}</li>
              </ul>
            </AlertDescription>
          </Alert>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
