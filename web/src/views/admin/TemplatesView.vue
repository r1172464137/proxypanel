<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { api } from '@/api/http'
import { adminApi } from '@/api/endpoints'
import { TEMPLATE_FORMATS, type TemplateFormat, type TemplateRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { isRecord, type JsonObject } from '@/lib/json'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import JsonEditor from '@/components/JsonEditor.vue'

const list = usePagedList<TemplateRecord>((cursor) => adminApi.templates.list({ limit: 50, cursor }))

const formOpen = ref(false)
const form = reactive<{
  id: string
  version: number
  name: string
  format: TemplateFormat
  description: string
  content: JsonObject
}>({ id: '', version: 0, name: '', format: 'xray', description: '', content: {} })
const saving = ref(false)
const formError = ref('')
const formErrors = ref<string[]>([])
const jsonError = ref('')

const deleteTarget = ref<TemplateRecord | null>(null)
const deleting = ref(false)
const deleteError = ref('')

onMounted(() => void list.refresh())

function openCreate(): void {
  Object.assign(form, {
    id: '',
    version: 0,
    name: '',
    format: 'xray' as TemplateFormat,
    description: '',
    content: {},
  })
  formErrors.value = []
  formError.value = ''
  jsonError.value = ''
  formOpen.value = true
}

function openEdit(template: TemplateRecord): void {
  Object.assign(form, {
    id: template.id,
    version: template.version,
    name: template.name,
    format: template.format,
    description: template.description ?? '',
    content: isRecord(template.content) ? { ...template.content } : {},
  })
  formErrors.value = []
  formError.value = ''
  jsonError.value = ''
  formOpen.value = true
}

function updatedAt(template: TemplateRecord): string {
  if (typeof template.updated_at === 'string') return formatDateTime(template.updated_at)
  if (typeof template.created_at === 'string') return formatDateTime(template.created_at)
  return '—'
}

function contentKeys(template: TemplateRecord): number {
  return isRecord(template.content) ? Object.keys(template.content).length : 0
}

async function save(): Promise<void> {
  formErrors.value = []
  formError.value = ''
  if (jsonError.value) {
    formError.value = '模板 content JSON 存在解析错误，未提交'
    return
  }
  if (!form.name.trim()) formErrors.value.push('模板名称必填')
  if (formErrors.value.length) return

  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      format: form.format,
      description: form.description.trim() || null,
      content: form.content,
    }
    if (form.id) {
      await adminApi.templates.update(form.id, { ...payload, version: form.version })
    } else {
      await adminApi.templates.create(payload)
    }
    formOpen.value = false
    pushToast('success', '模板已保存', '模板变更会使引用它的订阅输出重新渲染。')
    await list.refresh()
  } catch (caught) {
    formError.value = errorText(caught)
  } finally {
    saving.value = false
  }
}

// endpoints.ts 只封装了 templates 的 list/get/create/update；DELETE 已由后端挂载
// （internal/panel/api/api.go），这里按同一路径直接调用，避免改动共享的端点文件。
function removeTemplate(id: string): Promise<{ deleted?: boolean; id?: string }> {
  return api.delete<{ deleted?: boolean; id?: string }>(`/api/v1/admin/templates/${encodeURIComponent(id)}`)
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  deleteError.value = ''
  try {
    await removeTemplate(target.id)
    deleteTarget.value = null
    pushToast('success', '模板已删除', '删除会写入审计记录；引用该模板的设置需自行修正。')
    await list.refresh()
  } catch (caught) {
    deleteError.value = errorText(caught)
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div>
    <PageHeader title="模板" description="客户端输出模板（xray / mihomo / singbox）。模板内容按 JSON 原样保存；格式能力不兼容时服务端会明确报错。">
      <button type="button" class="btn btn--primary" @click="openCreate">新建模板</button>
    </PageHeader>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取模板" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="还没有客户端模板" description="模板决定订阅输出结构（xray / mihomo / singbox）。创建后可在订阅规则或设置中引用；节点参数无法被某格式表达时，服务端会说明不兼容的字段。">
          <button type="button" class="btn btn--primary" @click="openCreate">新建模板</button>
        </EmptyState>
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>格式</th>
                <th class="hide-sm">说明</th>
                <th class="hide-sm">更新时间</th>
                <th>版本</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="template in list.items.value" :key="template.id">
                <td>
                  {{ template.name }}
                  <span class="cell-sub mono">{{ template.id }}</span>
                </td>
                <td><span class="badge">{{ template.format }}</span></td>
                <td class="hide-sm text-sm">
                  {{ template.description || '—' }}
                  <span class="cell-sub">{{ contentKeys(template) }} 个顶层字段</span>
                </td>
                <td class="hide-sm">{{ updatedAt(template) }}</td>
                <td class="num">{{ template.version }}</td>
                <td class="actions">
                  <button type="button" class="btn btn--sm" @click="openEdit(template)">编辑</button>
                  <button type="button" class="btn btn--sm btn--danger" @click="deleteTarget = template">删除</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="row row--between" style="padding: var(--space-3) var(--space-4)">
          <span class="text-xs text-muted">第 {{ list.page.value }} 页</span>
          <div class="row">
            <button type="button" class="btn btn--sm" :disabled="list.page.value <= 1 || list.loading.value" @click="list.prev()">上一页</button>
            <button type="button" class="btn btn--sm" :disabled="!list.nextCursor.value || list.loading.value" @click="list.next()">下一页</button>
          </div>
        </div>
      </template>
    </div>

    <AppModal :open="formOpen" :title="form.id ? '编辑模板' : '新建模板'" size="lg" @close="formOpen = false">
      <div class="stack">
        <p v-if="formError" class="banner banner--danger" role="alert">{{ formError }}</p>
        <p v-for="error in formErrors" :key="error" class="banner banner--warn">{{ error }}</p>
        <div class="grid grid--form">
          <div class="field">
            <label for="tpl-name">名称</label>
            <input id="tpl-name" v-model="form.name" class="input" required />
          </div>
          <div class="field">
            <label for="tpl-format">格式</label>
            <select id="tpl-format" v-model="form.format" class="select">
              <option v-for="format in TEMPLATE_FORMATS" :key="format" :value="format">{{ format }}</option>
            </select>
          </div>
        </div>
        <div class="field">
          <label for="tpl-desc">说明</label>
          <textarea id="tpl-desc" v-model="form.description" class="textarea" style="min-height: 70px" />
        </div>
        <JsonEditor
          v-model="form.content"
          label="content"
          hint="模板内容结构由服务端契约决定；未知字段原样保留并提交。"
          @update:error="jsonError = $event"
        />
        <p v-if="form.id" class="text-xs text-muted">
          保存会提交当前版本号（{{ form.version }}）；版本已变化时服务端拒绝覆盖，请重新打开后再改。
        </p>
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="saving" @click="formOpen = false">取消</button>
        <button type="button" class="btn btn--primary" :disabled="saving" @click="save">
          <span v-if="saving" class="spinner" aria-hidden="true" />
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </template>
    </AppModal>

    <ConfirmDialog
      :open="deleteTarget !== null"
      title="删除模板"
      confirm-label="删除"
      danger
      :pending="deleting"
      :error-text="deleteError"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p>将删除模板 <strong>{{ deleteTarget?.name }}</strong>（<span class="mono">{{ deleteTarget?.id }}</span>）。</p>
      <p class="text-sm">引用该模板的订阅/设置不会被自动改写，可能输出失败；需要时请先切换到其它模板。</p>
    </ConfirmDialog>
  </div>
</template>
