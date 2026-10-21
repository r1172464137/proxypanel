<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { InvitationCreated, InvitationRecord, SubscriptionRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatDateTime, fromLocalInput } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import CopyField from '@/components/CopyField.vue'

const list = usePagedList<InvitationRecord>((cursor) => adminApi.invitations.list({ limit: 50, cursor }))
const subscriptions = ref<SubscriptionRecord[]>([])

const createOpen = ref(false)
const form = reactive({ subscription_id: '', expires_at_local: '' })
const created = ref<InvitationCreated | null>(null)
const creating = ref(false)
const createError = ref('')

const revokeTarget = ref<InvitationRecord | null>(null)
const revoking = ref(false)
const revokeError = ref('')

onMounted(async () => {
  void list.refresh()
  try {
    subscriptions.value = (await adminApi.subscriptions.list({ limit: 200 })).items
  } catch {
    subscriptions.value = []
  }
})

function openCreate(): void {
  form.subscription_id = ''
  form.expires_at_local = ''
  created.value = null
  createError.value = ''
  createOpen.value = true
}

async function create(): Promise<void> {
  creating.value = true
  createError.value = ''
  try {
    created.value = await adminApi.invitations.create({
      subscription_id: form.subscription_id || null,
      expires_at: fromLocalInput(form.expires_at_local),
    })
    pushToast('success', '邀请已创建', '邀请码只展示一次；列表接口不会泄露 code。')
    await list.refresh()
  } catch (caught) {
    createError.value = errorText(caught)
  } finally {
    creating.value = false
  }
}

async function revoke(): Promise<void> {
  const target = revokeTarget.value
  if (!target) return
  revoking.value = true
  revokeError.value = ''
  try {
    await adminApi.invitations.revoke(target.id)
    pushToast('success', '邀请已撤销', '未兑换的邀请将不可使用。')
    revokeTarget.value = null
    await list.refresh()
  } catch (caught) {
    revokeError.value = errorText(caught)
  } finally {
    revoking.value = false
  }
}

function statusOf(invitation: InvitationRecord): string {
  if (invitation.state) return invitation.state
  if (invitation.status) return invitation.status
  if (invitation.used_at) return 'used'
  if (invitation.expires_at && new Date(invitation.expires_at).getTime() < Date.now()) return 'expired'
  return 'unused'
}
</script>

<template>
  <div>
    <PageHeader title="邀请" description="一次性邀请码用于创建成员（不授予管理员）。兑换为原子操作；列表不展示 code。">
      <button type="button" class="btn btn--primary" @click="openCreate">生成邀请码</button>
    </PageHeader>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取邀请" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="还没有邀请记录" description="生成一次性邀请码并发给受邀者；兑换成功后成员可登录，但代理权限仍需绑定订阅。">
          <button type="button" class="btn btn--primary" @click="openCreate">生成邀请码</button>
        </EmptyState>
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>邀请</th>
                <th>状态</th>
                <th>使用 / 订阅</th>
                <th class="hide-sm">过期时间</th>
                <th class="hide-sm">创建时间</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="invitation in list.items.value" :key="invitation.id">
                <td class="mono">{{ invitation.id }}</td>
                <td><StatusBadge :status="statusOf(invitation)" /></td>
                <td class="num">{{ invitation.used_count ?? 0 }} / {{ invitation.max_uses ?? 1 }}<span class="cell-sub mono">{{ invitation.subscription_id ?? '未绑定' }}</span></td>
                <td class="hide-sm">{{ formatDateTime(invitation.expires_at) }}</td>
                <td class="hide-sm">{{ formatDateTime(invitation.created_at) }}</td>
                <td class="actions">
                  <button
                    type="button"
                    class="btn btn--sm btn--danger"
                    :disabled="statusOf(invitation) !== 'unused'"
                    @click="revokeTarget = invitation"
                  >
                    撤销
                  </button>
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

    <AppModal :open="createOpen" title="生成邀请码" size="sm" @close="createOpen = false">
      <div class="stack">
        <div class="field">
          <label for="invite-sub">预绑定订阅（可选）</label>
          <select id="invite-sub" v-model="form.subscription_id" class="select">
            <option value="">不预绑定</option>
            <option v-for="sub in subscriptions" :key="sub.id" :value="sub.id">{{ sub.name }}</option>
          </select>
        </div>
        <div class="field">
          <label for="invite-expiry">过期时间</label>
          <input id="invite-expiry" v-model="form.expires_at_local" class="input" type="datetime-local" />
          <p class="field__help">留空表示由服务端默认策略决定</p>
        </div>
        <p v-if="createError" class="banner banner--danger" role="alert">{{ createError }}</p>
        <template v-if="created">
          <div class="banner banner--info">邀请码只展示一次，关闭后列表不会再次显示 code。</div>
          <CopyField :value="created.code" label="邀请码" warning="邀请码等同于一次注册权限，仅交付给受邀者。" />
          <p class="text-xs text-muted">过期时间：{{ formatDateTime(created.expires_at) }}</p>
        </template>
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="creating" @click="createOpen = false">关闭</button>
        <button type="button" class="btn btn--primary" :disabled="creating" @click="create">
          <span v-if="creating" class="spinner" aria-hidden="true" />
          {{ created ? '再生成一个' : '生成' }}
        </button>
      </template>
    </AppModal>

    <AppModal :open="revokeTarget !== null" title="撤销邀请" size="sm" @close="revokeTarget = null">
      <div class="stack">
        <p>将撤销邀请 <span class="mono">{{ revokeTarget?.id }}</span>；已兑换的邀请不受影响。</p>
        <p v-if="revokeError" class="banner banner--danger" role="alert">{{ revokeError }}</p>
      </div>
      <template #footer>
        <button type="button" class="btn" :disabled="revoking" @click="revokeTarget = null">取消</button>
        <button type="button" class="btn btn--danger" :disabled="revoking" @click="revoke">确认撤销</button>
      </template>
    </AppModal>
  </div>
</template>