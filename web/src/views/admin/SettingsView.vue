<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { PanelSettings, TemplateFormat, TemplateRecord } from '@/api/types'
import { TEMPLATE_FORMATS } from '@/api/types'
import { errorText } from '@/lib/errors'
import { isValidTimezone } from '@/lib/format'
import { stringifyJson, isRecord, type JsonObject } from '@/lib/json'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import JsonEditor from '@/components/JsonEditor.vue'

const settings = ref<PanelSettings | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
const saving = ref(false)
const saveError = ref('')

const form = reactive({
  timezone: '',
  retention_days: '',
  telegram_enabled: false,
  telegram_chat_id: '',
  telegram_token: '',
})

const templateDefaultsKey = ref<string | null>(null)
const templateDefaults = ref<JsonObject>({})
const templateJsonError = ref('')

const templates = ref<TemplateRecord[]>([])
const templatesError = ref<unknown>(null)

const templateModal = ref(false)
const templateForm = reactive({
  id: '',
  version: 0,
  name: '',
  format: 'xray' as TemplateFormat,
  description: '',
  content: {} as JsonObject,
})
const templateSaving = ref(false)
const templateSaveError = ref('')
const templateJsonInvalid = ref('')

const recoveryState = ref<{ recovery_mode: boolean; control_generation: number } | null>(null)
const recoveryChecks = reactive({ agent_revisions_checked: false, ledger_checked: false })
const recovery = reactive<{ open: boolean; mode: 'enter' | 'exit' | null; pending: boolean; error: string }>({
  open: false,
  mode: null,
  pending: false,
  error: '',
})

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  try {
    const payload = await adminApi.settings.get()
    recoveryState.value = await adminApi.recoveryMode.get()
    settings.value = payload
    form.timezone = typeof payload.timezone === 'string' ? payload.timezone : ''
    form.retention_days = typeof payload.retention_days === 'number' ? String(payload.retention_days) : ''
    form.telegram_enabled = payload.telegram_enabled === true
    form.telegram_chat_id = typeof payload.telegram_chat_id === 'string' ? payload.telegram_chat_id : ''
    form.telegram_token = ''
    const key = ['template_defaults', 'templates', 'default_templates'].find(
      (candidate) => candidate in payload && isRecord(payload[candidate]),
    )
    templateDefaultsKey.value = key ?? null
    templateDefaults.value = key ? { ...(payload[key] as JsonObject) } : {}
    templateJsonError.value = ''
  } catch (caught) {
    loadError.value = caught
  } finally {
    loading.value = false
  }
}

async function loadTemplates(): Promise<void> {
  try {
    templates.value = (await adminApi.templates.list({ limit: 200 })).items
    templatesError.value = null
  } catch (caught) {
    templatesError.value = caught
    templates.value = []
  }
}

onMounted(async () => {
  await Promise.all([load(), loadTemplates()])
})

const tokenConfigured = computed(() => settings.value?.telegram_token_set === true)

async function save(): Promise<void> {
  saveError.value = ''
  if (templateJsonError.value) {
    saveError.value = '模板默认设置的 JSON 存在解析错误，未提交'
    return
  }
  if (form.timezone.trim() && !isValidTimezone(form.timezone.trim())) {
    saveError.value = `业务时区无效：${form.timezone}`
    return
  }
  if (form.retention_days.trim()) {
    const days = Number(form.retention_days)
    if (!Number.isInteger(days) || days < 1) {
      saveError.value = '保留天数必须是正整数'
      return
    }
  }
  saving.value = true
  try {
    // 只提交本页可编辑字段；留空的非必填项不发送，避免用默认值覆盖服务端设置。
    const body: PanelSettings = {
      // 并发控制：PUT 必须回传 GET 的 version（服务端拒绝旧版本覆盖）。
      version: settings.value?.version ?? 0,
      telegram_enabled: form.telegram_enabled,
      telegram_chat_id: form.telegram_chat_id.trim(),
    }
    if (form.timezone.trim()) body.timezone = form.timezone.trim()
    if (form.retention_days.trim()) body.retention_days = Number(form.retention_days)
    if (form.telegram_token.trim()) body.telegram_token = form.telegram_token.trim()
    if (templateDefaultsKey.value) body[templateDefaultsKey.value] = templateDefaults.value
    const updated = await adminApi.settings.update(body)
    settings.value = updated
    form.telegram_token = ''
    pushToast('success', '设置已保存', '设置变更写入审计；通知渠道启用前请先验证实际送达。')
    await load()
  } catch (caught) {
    saveError.value = errorText(caught)
  } finally {
    saving.value = false
  }
}

function openTemplateCreate(): void {
  Object.assign(templateForm, {
    id: '',
    version: 0,
    name: '',
    format: 'xray' as TemplateFormat,
    description: '',
    content: {},
  })
  templateSaveError.value = ''
  templateJsonInvalid.value = ''
  templateModal.value = true
}

function openTemplateEdit(template: TemplateRecord): void {
  Object.assign(templateForm, {
    id: template.id,
    version: template.version,
    name: template.name,
    format: template.format,
    description: template.description ?? '',
    content: isRecord(template.content) ? { ...template.content } : {},
  })
  templateSaveError.value = ''
  templateJsonInvalid.value = ''
  templateModal.value = true
}

async function saveTemplate(): Promise<void> {
  templateSaveError.value = ''
  if (templateJsonInvalid.value) {
    templateSaveError.value = '模板 content JSON 存在解析错误，未提交'
    return
  }
  if (!templateForm.name.trim()) {
    templateSaveError.value = '模板名称必填'
    return
  }
  templateSaving.value = true
  try {
    const payload = {
      name: templateForm.name.trim(),
      format: templateForm.format,
      description: templateForm.description.trim() || null,
      content: templateForm.content,
    }
    if (templateForm.id) {
      await adminApi.templates.update(templateForm.id, { ...payload, version: templateForm.version })
    } else {
      await adminApi.templates.create(payload)
    }
    templateModal.value = false
    pushToast('success', '模板已保存', '模板变更会使相关订阅输出的缓存失效。')
    await loadTemplates()
  } catch (caught) {
    templateSaveError.value = errorText(caught)
  } finally {
    templateSaving.value = false
  }
}

function openRecovery(mode: 'enter' | 'exit'): void {
  recovery.mode = mode
  recoveryChecks.agent_revisions_checked = false
  recoveryChecks.ledger_checked = false
  recovery.error = ''
  recovery.open = true
}

async function submitRecovery(): Promise<void> {
  if (!recovery.mode) return
  if (recovery.mode === 'exit' && (!recoveryChecks.agent_revisions_checked || !recoveryChecks.ledger_checked)) {
    recovery.error = '请先完成并勾选两项核对，再退出恢复协调模式。'
    return
  }
  recovery.pending = true
  recovery.error = ''
  try {
    if (recovery.mode === 'enter') await adminApi.recoveryMode.enter()
    else await adminApi.recoveryMode.exit({ ...recoveryChecks })
    recoveryState.value = await adminApi.recoveryMode.get()
    pushToast(
      'success',
      recovery.mode === 'enter' ? '已请求进入恢复协调模式' : '已请求退出恢复协调模式',
      '已读取服务端当前状态。新控制代际仍需在各 Agent 本地显式协调接受。',
    )
    recovery.open = false
  } catch (caught) {
    recovery.error = errorText(caught)
  } finally {
    recovery.pending = false
  }
}

const templateDefaultsPreview = computed(() => stringifyJson(templateDefaults.value))

// 判断这个字段是否真有内容（空对象算空）。
// 编辑器只在用户真正展开时挂载，避免折叠状态下也留一个空框在 DOM 里。
const templateEditorOpen = ref(false)

const hasTemplateDefaults = computed(() => {
  const value = templateDefaults.value
  return !!value && typeof value === 'object' && Object.keys(value).length > 0
})
</script>

<template>
  <div>
    <PageHeader title="设置" description="面板的全局设置：默认时区、通知、数据保留、客户端模板。">
      <span v-if="settings?.version !== undefined" class="text-xs text-muted">版本 {{ settings.version }}</span>
      <button type="button" class="btn" :disabled="loading" @click="load">重新读取</button>
      <button type="button" class="btn btn--primary" :disabled="saving" @click="save">
        <span v-if="saving" class="spinner" aria-hidden="true" />
        {{ saving ? '保存中…' : '保存设置' }}
      </button>
    </PageHeader>

    <LoadingBlock v-if="loading && !settings" label="正在读取设置" />
    <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />
    <div v-else class="stack">
      <p v-if="saveError" class="banner banner--danger" role="alert">{{ saveError }}</p>

      <section class="card">
        <div class="card__header"><h2>基础</h2></div>
        <div class="card__body grid grid--form">
          <div class="field">
            <label for="settings-tz">默认时区</label>
            <input id="settings-tz" v-model="form.timezone" class="input mono" placeholder="Asia/Shanghai" />
            <p class="field__help">订阅必须显式写入时区；此处仅默认值。</p>
          </div>
          <div class="field">
            <label for="settings-retention">数据保留天数</label>
            <input id="settings-retention" v-model="form.retention_days" class="input" inputmode="numeric" />
            <p class="field__help">高频监控数据聚合后清理；账本与审计按服务端策略保留。</p>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card__header">
          <h2>通知（Telegram）</h2>
          <div class="card__header-actions">
            <span class="badge" :class="tokenConfigured ? 'badge--ok' : 'badge--muted'">
              {{ tokenConfigured ? '已配置令牌' : '未配置令牌' }}
            </span>
          </div>
        </div>
        <div class="card__body stack">
          <label class="checkbox">
            <input v-model="form.telegram_enabled" type="checkbox" />
            启用 Telegram 通知（启用前请确认实际送达）
          </label>
          <div class="grid grid--form">
            <div class="field">
              <label for="settings-chat">Chat ID</label>
              <input id="settings-chat" v-model="form.telegram_chat_id" class="input mono" />
            </div>
            <div class="field">
              <label for="settings-token">Bot Token（仅写）</label>
              <input id="settings-token" v-model="form.telegram_token" class="input mono" type="password" autocomplete="off" placeholder="留空表示不修改" />
              <p class="field__help">服务端只返回“是否已设置”，不回显明文；保存后页面立即清除输入。</p>
            </div>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card__header"><h2>模板默认设置</h2></div>
        <div class="card__body stack">
          <!-- 这个字段是自由结构（后端类型就是 Object），当前一般为空。
               直接摊开一个空 JSON 编辑器会让人以为"必须填点什么"。
               有值时才给只读预览，需要改再手动展开编辑器。 -->
          <template v-if="!templateDefaultsKey">
            <p class="text-sm text-muted">当前服务端没有这个设置项，无需配置。</p>
          </template>
          <template v-else-if="!hasTemplateDefaults">
            <p class="text-sm">
              模板默认值为空。一般情况下这里不需要填写 —— 新建的客户端模板会自动带上合适的默认结构。
            </p>
            <details @toggle="templateEditorOpen = ($event.target as HTMLDetailsElement).open">
              <summary class="text-xs text-muted">确实需要手动设置？</summary>
              <!-- 只在展开后挂载编辑器：折叠状态下也会出现在 DOM 里，
                   表现为页面上多了一个空的 {} 框。 -->
              <div v-if="templateEditorOpen" style="margin-top: var(--space-2)">
                <JsonEditor
                  :model-value="templateDefaults"
                  label="模板默认值（高级）"
                  hint="填写后会影响新建模板的初始结构。不熟悉时请保持为空。"
                  @update:model-value="templateDefaults = $event"
                  @update:error="templateJsonError = $event"
                />
              </div>
            </details>
          </template>
          <template v-else>
            <p class="text-sm">当前已设置以下默认值。修改会写入新建的客户端模板。</p>
            <pre class="code-block">{{ templateDefaultsPreview }}</pre>
            <details @toggle="templateEditorOpen = ($event.target as HTMLDetailsElement).open">
              <summary class="text-xs text-muted">编辑</summary>
              <div v-if="templateEditorOpen" style="margin-top: var(--space-2)">
                <JsonEditor
                  :model-value="templateDefaults"
                  label="模板默认值（高级）"
                  hint="保存后立即生效。"
                  @update:model-value="templateDefaults = $event"
                  @update:error="templateJsonError = $event"
                />
              </div>
            </details>
          </template>
        </div>
      </section>

      <section class="card">
        <div class="card__header">
          <h2>客户端模板</h2>
          <div class="card__header-actions">
            <button type="button" class="btn btn--sm" @click="openTemplateCreate">新建模板</button>
          </div>
        </div>
        <ErrorState v-if="templatesError" :error="templatesError" @retry="loadTemplates" />
        <div v-else-if="!templates.length" class="card__body">
          <EmptyState title="还没有客户端模板" description="模板用于 xray / mihomo / singbox 输出；格式能力不兼容时服务端会明确报错。">
            <button type="button" class="btn btn--primary" @click="openTemplateCreate">新建模板</button>
          </EmptyState>
        </div>
        <div v-else class="table-wrap">
          <table class="table">
            <thead><tr><th>名称</th><th>格式</th><th class="hide-sm">说明</th><th>版本</th><th>操作</th></tr></thead>
            <tbody>
              <tr v-for="template in templates" :key="template.id">
                <td>{{ template.name }}<span class="cell-sub mono">{{ template.id }}</span></td>
                <td><span class="badge">{{ template.format }}</span></td>
                <td class="hide-sm text-sm">{{ template.description || '—' }}</td>
                <td class="num">{{ template.version }}</td>
                <td class="actions">
                  <button type="button" class="btn btn--sm" @click="openTemplateEdit(template)">编辑</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card">
        <div class="card__header"><h2>恢复协调模式</h2></div>
        <div class="card__body stack">
          <p class="text-sm text-muted">
            从旧备份恢复后必须进入恢复协调：暂停普通变更与旧权限推送，核对节点授权版本与账本后再显式退出。
            当前状态：{{ recoveryState ? (recoveryState.recovery_mode ? '恢复协调中' : '正常运行') : '未读取' }}；控制代际：{{ recoveryState?.control_generation ?? '未知' }}。
          </p>
          <div class="row">
            <button type="button" class="btn btn--danger" @click="openRecovery('enter')">进入恢复协调模式</button>
            <button type="button" class="btn" @click="openRecovery('exit')">退出恢复协调模式</button>
          </div>
        </div>
      </section>
    </div>

    <AppModal :open="templateModal" :title="templateForm.id ? '编辑模板' : '新建模板'" size="md" @close="templateModal = false">
      <div class="stack">
        <p v-if="templateSaveError" class="banner banner--danger" role="alert">{{ templateSaveError }}</p>
        <div class="grid grid--form">
          <div class="field">
            <label for="tpl-name">名称</label>
            <input id="tpl-name" v-model="templateForm.name" class="input" />
          </div>
          <div class="field">
            <label for="tpl-format">格式</label>
            <select id="tpl-format" v-model="templateForm.format" class="select">
              <option v-for="format in TEMPLATE_FORMATS" :key="format" :value="format">{{ format }}</option>
            </select>
          </div>
        </div>
        <div class="field">
          <label for="tpl-desc">说明</label>
          <textarea id="tpl-desc" v-model="templateForm.description" class="textarea" style="min-height: 60px" />
        </div>
        <JsonEditor
          v-model="templateForm.content"
          label="content"
          hint="模板内容结构由服务端契约决定；未知字段原样保留。"
          @update:error="templateJsonInvalid = $event"
        />
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="templateSaving" @click="templateModal = false">取消</button>
        <button type="button" class="btn btn--primary" :disabled="templateSaving" @click="saveTemplate">
          <span v-if="templateSaving" class="spinner" aria-hidden="true" />保存
        </button>
      </template>
    </AppModal>

    <ConfirmDialog
      :open="recovery.open"
      :title="recovery.mode === 'enter' ? '进入恢复协调模式' : '退出恢复协调模式'"
      :confirm-label="recovery.mode === 'enter' ? '确认进入' : '确认退出'"
      danger
      :pending="recovery.pending"
      :error-text="recovery.error"
      @cancel="recovery.open = false"
      @confirm="submitRecovery"
    >
      <template v-if="recovery.mode === 'enter'">
        <p>进入后：普通变更暂停、不再向节点推送旧权限，直到显式核对并退出。</p>
        <ul class="text-sm">
          <li>成员/订阅/节点的普通变更可能被拒绝。</li>
          <li>请先核对 Agent 已知授权版本、任务回执与账本。</li>
        </ul>
      </template>
      <template v-else>
        <p>退出前请确认：</p>
        <ul class="text-sm">
          <li>节点授权版本与面板一致，账本无缺口。</li>
          <li>旧备份中已撤销的成员不会被重新放行。</li>
        </ul>
        <label class="row"><input v-model="recoveryChecks.agent_revisions_checked" type="checkbox" />已核对 Agent 修订及已撤销权限，不会恢复旧授权</label>
        <label class="row"><input v-model="recoveryChecks.ledger_checked" type="checkbox" />已核对用量账本与待补报队列</label>
      </template>
    </ConfirmDialog>
  </div>
</template>