<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import type { BatchOperation, BatchPreview } from '@/api/types'
import { batchOperationMeta, newIdempotencyKey, previewSummary, submitRequest } from '@/lib/batch'
import { toApiError } from '@/lib/errors'
import { sessionState } from '@/stores/session'
import { clearPending, readPending, savePending, scopeDigest, type PendingBatch } from '@/lib/batchPending'
import { formatDateTime, prettyJson } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'
import {
  describeBlockReason,
  describeConsequences,
  describeSnapshotChanges,
  type ConsequenceText,
} from '@/lib/batchConsequences'
import type { PreviewItem } from '@/api/types'
import AppModal from './AppModal.vue'
import ErrorState from './ErrorState.vue'
import LoadingBlock from './LoadingBlock.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    operation: BatchOperation
    memberIds: string[]
    values: Record<string, unknown>
    recoverPending?: boolean
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

const idempotencyKey = ref('')
const uncertain = ref(false)
const stale = ref(false)
const reason = ref('')
const previewScope = ref('')
const scope = computed(() => JSON.stringify([props.operation, [...props.memberIds].sort(), props.values]))
const scopeChanged = computed(() => previewScope.value !== scope.value)

const meta = computed(() => batchOperationMeta(props.operation))
const accountId = computed(() => sessionState.status === 'ready' ? sessionState.user?.id ?? '' : '')
const pending = ref<PendingBatch | null>(null)
const recoverable = ref<PendingBatch[]>([])
const storageFailure = ref('')
const resultId = ref('')
const replayed = ref(false)
let generation = 0
const pendingScope = computed(() => `${props.operation}:${scopeDigest([props.operation, [...props.memberIds].sort(), props.values])}`)

function restore(record: PendingBatch): void {
  if (record.accountId !== accountId.value) return
  pending.value = record
  idempotencyKey.value = record.key
  uncertain.value = true
  confirmed.value = false
}

async function openDialog(): Promise<void> {
  generation++
  pending.value = null
  preview.value = null
  uncertain.value = false
  stale.value = false
  submitting.value = false
  loading.value = false
  confirmed.value = false
  reason.value = ''
  submitError.value = null
  error.value = null
  storageFailure.value = ''
  recoverable.value = []
  resultId.value = ''
  if (!accountId.value) {
    storageFailure.value = '当前账号未确认，无法安全恢复或提交。'
    return
  }
  try {
    recoverable.value = readPending(accountId.value)
    const existing = recoverable.value.find((r) => r.scope === pendingScope.value)
    if (props.recoverPending) return
    if (existing) restore(existing)
    else await runPreview()
  } catch (caught) {
    storageFailure.value = (caught as Error).message
  }
}

async function runPreview(): Promise<void> {
  if (loading.value || uncertain.value || submitting.value || storageFailure.value || !accountId.value) return
  const currentGeneration = generation
  idempotencyKey.value = newIdempotencyKey()
  stale.value = false
  previewScope.value = scope.value
  loading.value = true
  error.value = null
  submitError.value = null
  preview.value = null
  confirmed.value = false
  try {
    const result = await adminApi.batches.preview({
      operation: props.operation,
      member_ids: [...props.memberIds],
      values: { ...props.values },
      ...(reason.value ? { reason: reason.value } : {}),
    })
    if (currentGeneration === generation) preview.value = result
  } catch (caught) {
    if (currentGeneration === generation) error.value = caught
  } finally {
    if (currentGeneration === generation) loading.value = false
  }
}

watch(
  () => [props.open, accountId.value],
  () => {
    if (props.open) void openDialog()
    else generation++
  },
  { immediate: true },
)

async function submit(): Promise<void> {
  if (submitting.value || storageFailure.value || !accountId.value || resultId.value) return
  if (!pending.value) {
    if (!preview.value || !confirmed.value || stale.value || scopeChanged.value) return
    if (!(new Date(preview.value.expires_at).getTime() > Date.now())) {
      stale.value = true
      return
    }
    pending.value = {
      accountId: accountId.value, operation: props.operation, scope: pendingScope.value,
      targetDigest: scopeDigest([...props.memberIds].sort()), targetCount: props.memberIds.length,
      key: idempotencyKey.value, request: submitRequest(preview.value),
    }
  }
  const record = pending.value
  if (record.accountId !== accountId.value) return
  // 必须先写入且回读成功，再发送；不确定结果始终复用这份请求与 key。
  try {
    savePending(record)
  } catch (caught) {
    storageFailure.value = (caught as Error).message
    return
  }
  const currentGeneration = generation
  submitting.value = true
  submitError.value = null
  uncertain.value = true
  try {
    const result = await adminApi.batches.submit(record.request, record.key)
    if (currentGeneration !== generation || record.accountId !== accountId.value) return
    if (!result.id) throw new Error('提交响应缺少变更记录编号，结果仍未确认')
    resultId.value = result.id
    replayed.value = result.idempotent_replay === true
    uncertain.value = false
    try { clearPending(record) } catch (caught) { storageFailure.value = (caught as Error).message }
    pending.value = null
    pushToast('success', replayed.value ? '这是之前那次提交的结果' : '批量变更已提交', '请在变更记录中核对逐对象结果。')
    emit('submitted', result.id)
  } catch (caught) {
    if (currentGeneration !== generation || record.accountId !== accountId.value) return
    const failure = toApiError(caught)
    submitError.value = failure
    // 认证、限流及服务端错误不能证明原提交未发生，保留记录供原账号核对。
    const definitive = failure.status >= 400 && failure.status < 500 && ![401, 403, 408, 429].includes(failure.status)
    if (definitive) {
      uncertain.value = false
      stale.value = true
      try {
        clearPending(record)
        pending.value = null
        recoverable.value = recoverable.value.filter((r) => r.key !== record.key)
      } catch (caught) { storageFailure.value = (caught as Error).message }
    }
  } finally {
    if (currentGeneration === generation) submitting.value = false
  }
}

function exceptionText(item: unknown): string {
  if (typeof item === 'string') return item
  if (item && typeof item === 'object') {
    const record = item as Record<string, unknown>
    const label = record.member_id ?? record.node_id ?? record.id ?? record.object_id
    const reason = record.reason ?? record.message ?? record.code
    if (label || reason) {
      return `${String(label ?? '对象')}：${describeBlockReason(reason ? String(reason) : null)}`
    }
  }
  return prettyJson(item)
}

// 纯展示用：把预览里的四个数字整理成表格行（不改动任何请求/提交逻辑）。
// 每个成员的后果条目（已翻成中文）。
function consequencesOf(item: PreviewItem): ConsequenceText[] {
  return describeConsequences(item.consequences)
}

// before/after 的关键字段差异（人话）。
function changesOf(item: PreviewItem): string[] {
  return describeSnapshotChanges(item.before, item.after)
}

function toneClass(tone: ConsequenceText['tone']): string {
  switch (tone) {
    case 'danger':
      return 'text-destructive'
    case 'warn':
      return 'text-amber-600 dark:text-amber-400'
    case 'ok':
      return 'text-emerald-600 dark:text-emerald-400'
    default:
      return 'text-muted-foreground'
  }
}

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
  <AppModal :open="open" :title="title" size="lg" @close="!submitting && emit('close')">
    <div class="flex flex-col gap-4">
      <p class="text-sm text-muted-foreground">{{ description || meta.description }}</p>
      <p v-if="storageFailure" role="alert">{{ storageFailure }}</p>
      <section v-if="recoverPending && !pending && !resultId" class="space-y-2">
        <p>当前账号未确认提交（{{ recoverable.length }} 项）。核对沿用原预览与幂等键，不创建新变更。</p>
        <Button v-for="record in recoverable" :key="record.key" variant="outline" @click="restore(record)">
          核对 {{ batchOperationMeta(record.operation).label }} · {{ record.targetCount }} 名 · {{ record.targetDigest }}
        </Button>
      </section>
      <p v-if="pending" role="status">原提交预览 {{ pending.request.preview_id }} · 本次目标 {{ pending.targetCount }} 名 · 摘要 {{ pending.targetDigest }}</p>
      <p v-if="resultId" role="status">{{ replayed ? '这是之前那次提交的结果' : '批量变更已提交' }}。<a :href="`/admin/batches/${encodeURIComponent(resultId)}`" @click.prevent="router.push(`/admin/batches/${encodeURIComponent(resultId)}`)">查看本次变更记录</a></p>
      <Label v-if="!recoverPending && !pending && !resultId">变更原因<input v-model="reason" :disabled="submitting || uncertain" class="mt-1 block w-full rounded border p-2" aria-label="变更原因" /></Label>
      <p v-if="uncertain" role="status">结果未确认。请核对同一次提交；不要重新创建变更。记录已保存，刷新或返回成员页后可从“核对未确认批量提交”继续。</p>
      <p v-if="!pending && !recoverPending && (stale || scopeChanged)" role="alert">影响或目标范围已变化，请重新预览。</p>

      <LoadingBlock v-if="loading" :rows="3" label="正在请求影响预览" />
      <ErrorState v-else-if="error" :error="error" retry-label="重新预览" @retry="runPreview" />

      <template v-else-if="preview">
        <Alert>
          <AlertTitle>{{ resultId ? '原提交预览（已取得变更记录）' : pending ? '原提交预览（结果核对中）' : '预览结果（未提交）' }}</AlertTitle>
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

        <!-- 逐成员明细：预览的核心价值在这里——只给汇总数字等于没有预览。
             服务端返回 before/after 快照与 consequences 码，这里翻成中文并列出来。 -->
        <div v-if="preview.items?.length" class="space-y-2">
          <h3 class="text-sm font-medium">逐个成员的影响</h3>
          <div class="max-h-80 overflow-auto rounded-md border border-border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead class="w-40">成员</TableHead>
                  <TableHead>会发生什么</TableHead>
                  <TableHead class="w-24 text-right">节点数</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="item in preview.items" :key="item.member_id">
                  <TableCell class="font-medium">
                    {{ item.username || item.member_id }}
                    <Badge v-if="item.blocked" variant="destructive" class="ml-1">不可执行</Badge>
                  </TableCell>
                  <TableCell class="text-sm">
                    <ul v-if="consequencesOf(item).length" class="list-disc pl-4">
                      <li
                        v-for="(c, ci) in consequencesOf(item)"
                        :key="ci"
                        :class="toneClass(c.tone)"
                      >{{ c.label }}</li>
                    </ul>
                    <ul v-if="changesOf(item).length" class="list-disc pl-4 text-muted-foreground">
                      <li v-for="(change, ci) in changesOf(item)" :key="`c${ci}`">{{ change }}</li>
                    </ul>
                    <span v-if="item.blocked && item.blocked_reason" class="text-destructive">
                      {{ describeBlockReason(item.blocked_reason) }}
                    </span>
                    <span
                      v-if="!consequencesOf(item).length && !changesOf(item).length"
                      class="text-muted-foreground"
                    >
                      无变化
                    </span>
                  </TableCell>
                  <TableCell class="text-right tabular text-sm">
                    <template v-if="typeof item.nodes_removed === 'number' && item.nodes_removed > 0">
                      <span class="text-destructive">-{{ item.nodes_removed }}</span>
                    </template>
                    <template v-else-if="typeof item.nodes_added === 'number' && item.nodes_added > 0">
                      <span class="text-emerald-600 dark:text-emerald-400">+{{ item.nodes_added }}</span>
                    </template>
                    <span v-else class="text-muted-foreground">—</span>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </div>
        </div>

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

        <Label v-if="!pending && !resultId" class="flex items-start gap-2 text-sm font-normal">
          <input v-model="confirmed" type="checkbox" class="mt-1 size-4 accent-primary" />
          <span>我已确认以上影响范围（规则提交后立即生效，节点异步分发，可能中断连接）</span>
        </Label>
      </template>
      <ErrorState v-if="submitError" :error="submitError" :show-retry="false" />
    </div>

    <template #footer>
      <Button variant="outline" :disabled="submitting" @click="emit('close')">{{ resultId ? '关闭' : '取消' }}</Button>
      <Button v-if="!recoverPending && !resultId" variant="outline" :disabled="loading || submitting || uncertain || !!storageFailure" @click="runPreview">重新预览</Button>
      <Button v-if="!resultId && (!recoverPending || pending)" :disabled="!!storageFailure || submitting || (!pending && (!preview || !confirmed || stale || scopeChanged))" @click="submit">
        <Loader2 v-if="submitting" class="animate-spin" aria-hidden="true" />
        {{ submitting ? '正在核对…' : uncertain ? '核对同一次提交' : '确认提交' }}
      </Button>
    </template>
  </AppModal>
</template>
