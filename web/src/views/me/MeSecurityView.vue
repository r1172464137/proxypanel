<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { meApi } from '@/api/endpoints'
import type { SessionRecord } from '@/api/types'
import { useResource } from '@/lib/async'
import { errorText } from '@/lib/errors'
import { formatDateTime, formatRelative } from '@/lib/format'
import { clearSession } from '@/stores/session'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'

const router = useRouter()
const sessions = useResource<SessionRecord[]>(async () => (await meApi.sessions()).items)

const form = reactive({ current: '', next: '', confirm: '' })
const saving = ref(false)
const formError = ref('')
const localError = ref('')

const revokeTarget = ref<SessionRecord | null>(null)
const revoking = ref(false)
const revokeError = ref('')

onMounted(() => void sessions.reload())

const isCurrent = (session: SessionRecord): boolean => session.current === true

async function changePassword(): Promise<void> {
  localError.value = ''
  formError.value = ''
  if (form.next.length < 10) {
    localError.value = '新密码至少 10 位'
    return
  }
  if (form.next !== form.confirm) {
    localError.value = '两次输入的新密码不一致'
    return
  }
  saving.value = true
  try {
    await meApi.changePassword(form.current, form.next)
    form.current = ''
    form.next = ''
    form.confirm = ''
    pushToast('success', '密码已更新', '如服务端撤销了其它会话，列表会相应减少；当前会话可能仍需重新登录。')
    await sessions.reload()
  } catch (caught) {
    formError.value = errorText(caught)
  } finally {
    saving.value = false
  }
}

async function revoke(): Promise<void> {
  const target = revokeTarget.value
  if (!target) return
  revoking.value = true
  revokeError.value = ''
  try {
    await meApi.revokeSession(target.id)
    revokeTarget.value = null
    pushToast('success', '会话已撤销', isCurrent(target) ? '当前会话已退出。' : '该设备的登录已失效。')
    if (isCurrent(target)) {
      clearSession()
      await router.push({ name: 'login' })
      return
    }
    await sessions.reload()
  } catch (caught) {
    revokeError.value = errorText(caught)
  } finally {
    revoking.value = false
  }
}

const sessionCount = computed(() => sessions.data.value?.length ?? 0)
</script>

<template>
  <div>
    <PageHeader title="账号安全" description="修改密码与撤销会话；会话列表仅包含你本人的记录。">
      <button type="button" class="btn" :disabled="sessions.loading.value" @click="sessions.reload()">刷新会话</button>
    </PageHeader>

    <div class="stack">
      <section class="card">
        <div class="card__header"><h2>修改密码</h2></div>
        <div class="card__body" style="max-width: 520px">
          <form class="stack" @submit.prevent="changePassword">
            <p v-if="localError" class="banner banner--warn" role="alert">{{ localError }}</p>
            <p v-if="formError" class="banner banner--danger" role="alert">{{ formError }}</p>
            <div class="field">
              <label for="pw-current">当前密码</label>
              <input id="pw-current" v-model="form.current" class="input" type="password" autocomplete="current-password" required />
            </div>
            <div class="field">
              <label for="pw-next">新密码</label>
              <input id="pw-next" v-model="form.next" class="input" type="password" autocomplete="new-password" required minlength="10" />
              <p class="field__help">至少 10 位；服务端可能撤销其它会话</p>
            </div>
            <div class="field">
              <label for="pw-confirm">确认新密码</label>
              <input id="pw-confirm" v-model="form.confirm" class="input" type="password" autocomplete="new-password" required />
            </div>
            <button type="submit" class="btn btn--primary" :disabled="saving || !form.current || !form.next">
              <span v-if="saving" class="spinner" aria-hidden="true" />
              {{ saving ? '提交中…' : '更新密码' }}
            </button>
          </form>
        </div>
      </section>

      <section class="card">
        <div class="card__header">
          <h2>会话</h2>
          <div class="card__header-actions"><span class="text-xs text-muted">共 {{ sessionCount }} 个</span></div>
        </div>
        <LoadingBlock v-if="sessions.loading.value && !sessions.data.value" :rows="2" label="正在读取会话" />
        <ErrorState v-else-if="sessions.error.value" :error="sessions.error.value" @retry="sessions.reload()" />
        <div v-else-if="!sessionCount" class="card__body">
          <EmptyState title="没有活动会话" description="如果这里为空但你能看到本页，说明会话接口未返回记录；请联系管理员。" />
        </div>
        <div v-else class="table-wrap">
          <table class="table">
            <thead><tr><th>会话</th><th>创建</th><th>最近活动</th><th>来源</th><th>操作</th></tr></thead>
            <tbody>
              <tr v-for="session in sessions.data.value" :key="session.id">
                <td class="mono">{{ session.id }}<span v-if="isCurrent(session)" class="badge badge--accent" style="margin-left: 6px">当前</span></td>
                <td>{{ formatDateTime(session.created_at) }}</td>
                <td>
                  {{ formatDateTime(session.last_seen_at) }}
                  <span class="cell-sub">{{ formatRelative(session.last_seen_at) }}</span>
                </td>
                <td class="text-xs">{{ session.ip ?? '—' }}<span class="cell-sub break-all">{{ session.user_agent ?? '' }}</span></td>
                <td class="actions">
                  <button type="button" class="btn btn--sm btn--danger" @click="revokeTarget = session">撤销</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <ConfirmDialog
      :open="revokeTarget !== null"
      title="撤销会话"
      confirm-label="撤销"
      danger
      :pending="revoking"
      :error-text="revokeError"
      @cancel="revokeTarget = null"
      @confirm="revoke"
    >
      <p>将撤销会话 <span class="mono">{{ revokeTarget?.id }}</span>。</p>
      <p v-if="revokeTarget && isCurrent(revokeTarget)" class="text-sm">这是当前会话：撤销后你会被退出登录。</p>
      <p v-else class="text-sm">该设备上的登录立即失效，不影响其它会话。</p>
    </ConfirmDialog>
  </div>
</template>