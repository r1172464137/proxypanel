<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import type { InvitationCreated, InvitationRecord, SubscriptionRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText } from '@/lib/errors'
import { formatDateTime, fromLocalInput } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppModal from '@/components/AppModal.vue'
import CopyField from '@/components/CopyField.vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

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

// 表单里的下拉保持原生 select：jsdom 测试可直接设值，行为与重写前一致；
// 外观用 shadcn 的输入框样式变量。
const selectClass =
  'flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2'

const columns = [
  { label: '邀请', width: 'w-48' },
  { label: '状态', width: 'w-28' },
  { label: '使用 / 订阅' },
  { label: '过期时间', width: 'w-40', hideOnMobile: true },
  { label: '创建时间', width: 'w-40', hideOnMobile: true },
  { label: '操作', width: 'w-24' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="邀请" description="生成一次性邀请码发给朋友，对方用它自助注册。码只能用一次，用完即失效。">
      <Button @click="openCreate">生成邀请码</Button>
    </PageHeader>

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有邀请记录"
      empty-description="生成一次性邀请码并发给受邀者；兑换成功后成员可登录，但代理权限仍需绑定订阅。"
      loading-label="正在读取邀请"
      :count-label="`本页 ${list.items.value.length} 条`"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <span class="font-mono text-xs">{{ item.id }}</span>
        </template>
        <template v-else-if="columnIndex === 1">
          <StatusBadge :status="statusOf(item)" />
        </template>
        <template v-else-if="columnIndex === 2">
          {{ item.used_count ?? 0 }} / {{ item.max_uses ?? 1 }}
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.subscription_id ?? '未绑定' }}</span>
        </template>
        <template v-else-if="columnIndex === 3">{{ formatDateTime(item.expires_at) }}</template>
        <template v-else-if="columnIndex === 4">{{ formatDateTime(item.created_at) }}</template>
        <template v-else>
          <Button
            size="sm"
            variant="destructive"
            :disabled="statusOf(item) !== 'unused'"
            @click="revokeTarget = item"
          >
            撤销
          </Button>
        </template>
      </template>
      <template #empty-action>
        <Button @click="openCreate">生成邀请码</Button>
      </template>
    </DataTable>

    <AppModal :open="createOpen" title="生成邀请码" size="sm" @close="createOpen = false">
      <div class="space-y-4">
        <div class="space-y-1.5">
          <Label for="invite-sub">预绑定订阅（可选）</Label>
          <select id="invite-sub" v-model="form.subscription_id" :class="selectClass">
            <option value="">不预绑定</option>
            <option v-for="sub in subscriptions" :key="sub.id" :value="sub.id">{{ sub.name }}</option>
          </select>
        </div>
        <div class="space-y-1.5">
          <Label for="invite-expiry">过期时间</Label>
          <Input id="invite-expiry" v-model="form.expires_at_local" type="datetime-local" />
          <p class="text-xs text-muted-foreground">留空表示由服务端默认策略决定</p>
        </div>
        <Alert v-if="createError" variant="destructive" role="alert">
          <AlertDescription class="break-all">{{ createError }}</AlertDescription>
        </Alert>
        <template v-if="created">
          <div class="rounded-lg border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-sky-800 dark:border-sky-900 dark:bg-sky-950 dark:text-sky-200">
            邀请码只展示一次，关闭后列表不会再次显示 code。
          </div>
          <CopyField :value="created.code" label="邀请码" warning="邀请码等同于一次注册权限，仅交付给受邀者。" />
          <p class="text-xs text-muted-foreground">过期时间：{{ formatDateTime(created.expires_at) }}</p>
        </template>
      </div>
      <template #footer>
        <Button variant="outline" :disabled="creating" @click="createOpen = false">关闭</Button>
        <Button :disabled="creating" @click="create">
          <Loader2 v-if="creating" class="animate-spin" aria-hidden="true" />
          {{ created ? '再生成一个' : '生成' }}
        </Button>
      </template>
    </AppModal>

    <AppModal :open="revokeTarget !== null" title="撤销邀请" size="sm" @close="revokeTarget = null">
      <div class="space-y-4">
        <p>将撤销邀请 <span class="font-mono text-sm">{{ revokeTarget?.id }}</span>；已兑换的邀请不受影响。</p>
        <Alert v-if="revokeError" variant="destructive" role="alert">
          <AlertDescription class="break-all">{{ revokeError }}</AlertDescription>
        </Alert>
      </div>
      <template #footer>
        <Button variant="outline" :disabled="revoking" @click="revokeTarget = null">取消</Button>
        <Button variant="destructive" :disabled="revoking" @click="revoke">确认撤销</Button>
      </template>
    </AppModal>
  </div>
</template>
