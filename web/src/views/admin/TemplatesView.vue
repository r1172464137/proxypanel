<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { api } from '@/api/http'
import { adminApi } from '@/api/endpoints'
import { TEMPLATE_FORMATS, type TemplateFormat, type TemplateRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { isRecord, type JsonObject } from '@/lib/json'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import JsonEditor from '@/components/JsonEditor.vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

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

// 原生 select：既有 jsdom 测试直接对 #tpl-format 设值；外观沿用 shadcn 输入框变量。
const selectClass =
  'flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2'

const columns = [
  { label: '名称', width: 'w-56' },
  { label: '格式', width: 'w-24' },
  { label: '说明', hideOnMobile: true },
  { label: '更新时间', width: 'w-40', hideOnMobile: true },
  { label: '版本', width: 'w-16', numeric: true },
  { label: '操作', width: 'w-40' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="模板" description="客户端输出模板（xray / mihomo / singbox）。模板内容按 JSON 原样保存；格式能力不兼容时服务端会明确报错。">
      <Button @click="openCreate">新建模板</Button>
    </PageHeader>

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有客户端模板"
      empty-description="模板决定订阅输出结构（xray / mihomo / singbox）。创建后可在订阅规则或设置中引用；节点参数无法被某格式表达时，服务端会说明不兼容的字段。"
      loading-label="正在读取模板"
      :count-label="`本页 ${list.items.value.length} 个模板`"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <span class="font-medium">{{ item.name }}</span>
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.id }}</span>
        </template>
        <template v-else-if="columnIndex === 1">
          <Badge variant="outline" class="font-mono">{{ item.format }}</Badge>
        </template>
        <template v-else-if="columnIndex === 2">
          <span class="text-sm">{{ item.description || '—' }}</span>
          <span class="mt-0.5 block text-xs text-muted-foreground">{{ contentKeys(item) }} 个顶层字段</span>
        </template>
        <template v-else-if="columnIndex === 3">{{ updatedAt(item) }}</template>
        <template v-else-if="columnIndex === 4">{{ item.version }}</template>
        <template v-else>
          <div class="flex flex-wrap items-center gap-1.5">
            <Button size="sm" variant="outline" @click="openEdit(item)">编辑</Button>
            <Button size="sm" variant="destructive" @click="deleteTarget = item">删除</Button>
          </div>
        </template>
      </template>
      <template #empty-action>
        <Button @click="openCreate">新建模板</Button>
      </template>
    </DataTable>

    <AppModal :open="formOpen" :title="form.id ? '编辑模板' : '新建模板'" size="lg" @close="formOpen = false">
      <div class="space-y-4">
        <Alert v-if="formError" variant="destructive" role="alert">
          <AlertDescription class="break-all">{{ formError }}</AlertDescription>
        </Alert>
        <Alert v-for="error in formErrors" :key="error" variant="destructive" role="alert">
          <AlertDescription>{{ error }}</AlertDescription>
        </Alert>

        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label for="tpl-name">名称</Label>
            <Input id="tpl-name" v-model="form.name" required />
          </div>
          <div class="space-y-1.5">
            <Label for="tpl-format">格式</Label>
            <select id="tpl-format" v-model="form.format" :class="selectClass">
              <option v-for="format in TEMPLATE_FORMATS" :key="format" :value="format">{{ format }}</option>
            </select>
          </div>
        </div>

        <div class="space-y-1.5">
          <Label for="tpl-desc">说明</Label>
          <Textarea id="tpl-desc" v-model="form.description" class="min-h-[70px]" />
        </div>

        <JsonEditor
          v-model="form.content"
          label="content"
          hint="模板内容结构由服务端契约决定；未知字段原样保留并提交。"
          @update:error="jsonError = $event"
        />

        <p v-if="form.id" class="text-xs text-muted-foreground">
          保存会提交当前版本号（{{ form.version }}）；版本已变化时服务端拒绝覆盖，请重新打开后再改。
        </p>
      </div>
      <template #footer>
        <Button variant="outline" :disabled="saving" @click="formOpen = false">取消</Button>
        <Button :disabled="saving" @click="save">
          <Loader2 v-if="saving" class="animate-spin" aria-hidden="true" />
          {{ saving ? '保存中…' : '保存' }}
        </Button>
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
      <p>将删除模板 <strong>{{ deleteTarget?.name }}</strong>（<span class="font-mono">{{ deleteTarget?.id }}</span>）。</p>
      <p class="text-sm">引用该模板的订阅/设置不会被自动改写，可能输出失败；需要时请先切换到其它模板。</p>
    </ConfirmDialog>
  </div>
</template>
