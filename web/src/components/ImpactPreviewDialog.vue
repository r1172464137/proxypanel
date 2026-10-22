<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import type { BatchOperation, BatchPreview } from '@/api/types'
import { batchOperationMeta, newIdempotencyKey, previewSummary, submitRequest } from '@/lib/batch'
import { toApiError } from '@/lib/errors'
import { formatDateTime, prettyJson } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableRow } from '@/components/ui/table'
import AppModal from './AppModal.vue'
import ErrorState from './ErrorState.vue'
import LoadingBlock from './LoadingBlock.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    operation: BatchOperation
    memberIds: string[]
    values: Record<string, unknown>
    title?: string
    description?: string
  }>(),
  { title: '影响预览', description: '' },
)
const emit = defineEmits<{ close: []; submitted: [batchId: string] }>()

const router = useRouter()
const preview = ref<BatchPreview | null>(null)
const loading = ref(false)
const error = ref<unknown>(null)
const confirmed = ref(false)
const submitting = ref(false)
const submitError = ref<unknown>(null)

const meta = computed(() => batchOperationMeta(props.operation))

async function runPreview(): Promise<void> {
  loading.value = true
  error.value = null
  submitError.value = null
  preview.value = null
  confirmed.value = false
  try {
    preview.value = await adminApi.batches.preview({
      operation: props.operation,
      member_ids: [...props.memberIds],
      values: { ...props.values },
    })
  } catch (caught) {
    error.value = caught
  } finally {
    loading.value = false
  }
}

watch(
  () => props.open,
  (open) => {
    if (open) void runPreview()
  },
)

async function submit(): Promise<void> {
  if (!preview.value || !confirmed.value) return
  submitting.value = true
  submitError.value = null
  try {
    const result = await adminApi.batches.submit(submitRequest(preview.value), newIdempotencyKey())
    pushToast(
      'success',
      '批量变更已提交',
      '业务规则已在面板事务内提交；节点下发为异步，逐对象回执见批量结果页。',
    )
    emit('submitted', result.id)
    emit('close')
    await router.push(`/admin/batches/${encodeURIComponent(result.id)}`)
  } catch (caught) {
    submitError.value = toApiError(caught)
  } finally {
    submitting.value = false
  }
}

function exceptionText(item: unknown): string {
  if (typeof item === 'string') return item
  if (item && typeof item === 'object') {
    const record = item as Record<string, unknown>
    const label = record.member_id ?? record.node_id ?? record.id ?? record.object_id
    const reason = record.reason ?? record.message ?? record.code
    if (label || reason) return `${String(label ?? '对象')}：${String(reason ?? '不可执行')}`
  }
  return prettyJson(item)
}

// 纯展示用：把预览里的四个数字整理成表格行（不改动任何请求/提交逻辑）。
const metricRows = computed(() => {
  const current = preview.value
  if (!current) return []
  return [
    {
      key: 'members',
      label: '影响成员',
      value: `${current.affected_count} 名`,
      note: props.memberIds.length ? `已选择 ${props.memberIds.length} 名` : '',
    },
    { key: 'nodes', label: '受影响节点', value: `${current.affected_nodes} 个`, note: '' },
    { key: 'exceptions', label: '不可执行', value: `${current.exceptions.length} 项`, note: '' },
    { key: 'warnings', label: '警告', value: `${current.warnings.length} 条`, note: '' },
  ]
})
</script>

<template>
  <AppModal :open="open" :title="title" size="lg" @close="emit('close')">
    <div class="flex flex-col gap-4">
      <p class="text-sm text-muted-foreground">{{ description || meta.description }}</p>

      <LoadingBlock v-if="loading" :rows="3" label="正在请求影响预览" />
      <ErrorState v-else-if="error" :error="error" retry-label="重新预览" @retry="runPreview" />

      <template v-else-if="preview">
        <Alert>
          <AlertTitle>预览结果（未提交）</AlertTitle>
          <AlertDescription class="flex flex-col gap-1">
            <span>{{ previewSummary(preview) }}</span>
            <span class="text-xs">
              预览版本 {{ preview.version }} · 有效至
              <span :title="preview.expires_at">{{ formatDateTime(preview.expires_at) }}</span>
            </span>
          </AlertDescription>
        </Alert>

        <Table>
          <TableBody>
            <TableRow v-for="row in metricRows" :key="row.key">
              <TableCell class="w-40 text-sm text-muted-foreground">{{ row.label }}</TableCell>
              <TableCell class="text-sm font-medium tabular-nums">
                {{ row.value }}
                <span v-if="row.note" class="ml-2 text-xs font-normal text-muted-foreground">{{ row.note }}</span>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>

        <Alert v-if="preview.warnings.length">
          <AlertTitle>后果提示</AlertTitle>
          <AlertDescription>
            <ul class="list-disc pl-4">
              <li v-for="(warning, index) in preview.warnings" :key="index">{{ warning }}</li>
            </ul>
          </AlertDescription>
        </Alert>

        <Alert v-if="preview.exceptions.length" variant="destructive">
          <AlertTitle>不可执行对象（提交时会被跳过或拒绝）</AlertTitle>
          <AlertDescription>
            <ul class="list-disc pl-4">
              <li v-for="(item, index) in preview.exceptions" :key="index" class="break-all">
                {{ exceptionText(item) }}
              </li>
            </ul>
          </AlertDescription>
        </Alert>

        <details class="text-sm">
          <summary class="cursor-pointer text-muted-foreground">查看服务端返回的完整预览数据</summary>
          <pre
            class="mt-2 max-h-60 overflow-auto rounded-md border border-border bg-muted/50 p-3 font-mono text-xs"
          >{{ prettyJson(preview) }}</pre>
        </details>

        <Label class="flex items-start gap-2 text-sm font-normal">
          <input v-model="confirmed" type="checkbox" class="mt-1 size-4 accent-primary" />
          <span>我已确认以上影响范围（规则提交后立即生效，节点异步分发，可能中断连接）</span>
        </Label>

        <ErrorState v-if="submitError" :error="submitError" :show-retry="false" />
      </template>
    </div>

    <template #footer>
      <Button variant="outline" :disabled="submitting" @click="emit('close')">取消</Button>
      <Button variant="outline" :disabled="loading || submitting" @click="runPreview">重新预览</Button>
      <Button :disabled="!preview || !confirmed || submitting" @click="submit">
        <Loader2 v-if="submitting" class="animate-spin" aria-hidden="true" />
        {{ submitting ? '提交中…' : '确认提交' }}
      </Button>
    </template>
  </AppModal>
</template>
