<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import type { BatchOperation, BatchPreview } from '@/api/types'
import { batchOperationMeta, newIdempotencyKey, previewSummary, submitRequest } from '@/lib/batch'
import { toApiError } from '@/lib/errors'
import { formatDateTime, prettyJson } from '@/lib/format'
import { pushToast } from '@/lib/toast'
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
</script>

<template>
  <AppModal :open="open" :title="title" size="lg" @close="emit('close')">
    <div class="stack">
      <p class="text-sm text-muted">{{ description || meta.description }}</p>

      <LoadingBlock v-if="loading" :rows="3" label="正在请求影响预览" />
      <ErrorState v-else-if="error" :error="error" retry-label="重新预览" @retry="runPreview" />

      <template v-else-if="preview">
        <div class="banner banner--warn">
          <div class="stack--tight">
            <strong>预览结果（未提交）</strong>
            <span>{{ previewSummary(preview) }}</span>
            <span class="text-xs">
              预览版本 {{ preview.version }} · 有效至
              <span :title="preview.expires_at">{{ formatDateTime(preview.expires_at) }}</span>
            </span>
          </div>
        </div>

        <dl class="dl">
          <dt>影响成员</dt>
          <dd class="num">{{ preview.affected_count }} 名<template v-if="memberIds.length">（已选择 {{ memberIds.length }} 名）</template></dd>
          <dt>受影响节点</dt>
          <dd class="num">{{ preview.affected_nodes }} 个</dd>
          <dt>不可执行</dt>
          <dd class="num">{{ preview.exceptions.length }} 项</dd>
          <dt>警告</dt>
          <dd class="num">{{ preview.warnings.length }} 条</dd>
        </dl>

        <div v-if="preview.warnings.length" class="banner banner--warn">
          <div>
            <div class="banner__title">后果提示</div>
            <ul>
              <li v-for="(warning, index) in preview.warnings" :key="index">{{ warning }}</li>
            </ul>
          </div>
        </div>

        <div v-if="preview.exceptions.length" class="banner banner--danger">
          <div>
            <div class="banner__title">不可执行对象（提交时会被跳过或拒绝）</div>
            <ul>
              <li v-for="(item, index) in preview.exceptions" :key="index">{{ exceptionText(item) }}</li>
            </ul>
          </div>
        </div>

        <details>
          <summary class="text-sm text-muted">查看服务端返回的完整预览数据</summary>
          <pre class="code-block">{{ prettyJson(preview) }}</pre>
        </details>

        <label class="checkbox">
          <input v-model="confirmed" type="checkbox" />
          我已确认以上影响范围（规则提交后立即生效，节点异步分发，可能中断连接）
        </label>

        <ErrorState v-if="submitError" :error="submitError" :show-retry="false" />
      </template>
    </div>
    <template #footer>
      <button type="button" class="btn" :disabled="submitting" @click="emit('close')">取消</button>
      <button type="button" class="btn" :disabled="loading || submitting" @click="runPreview">重新预览</button>
      <button type="button" class="btn btn--primary" :disabled="!preview || !confirmed || submitting" @click="submit">
        <span v-if="submitting" class="spinner" aria-hidden="true" />
        {{ submitting ? '提交中…' : '确认提交' }}
      </button>
    </template>
  </AppModal>
</template>