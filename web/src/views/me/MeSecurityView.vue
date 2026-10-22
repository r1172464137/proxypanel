<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Loader2 } from 'lucide-vue-next'
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
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

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
  <div class="space-y-6">
    <PageHeader title="账号安全" description="修改密码与撤销会话；会话列表仅包含你本人的记录。">
      <Button variant="outline" :disabled="sessions.loading.value" @click="sessions.reload()">刷新会话</Button>
    </PageHeader>

    <div class="space-y-6">
      <Card>
        <CardHeader class="border-b">
          <CardTitle class="text-base font-semibold">修改密码</CardTitle>
        </CardHeader>
        <CardContent class="pt-6">
          <form class="flex max-w-lg flex-col gap-4" @submit.prevent="changePassword">
            <Alert
              v-if="localError"
              class="border-amber-300/70 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/50 dark:text-amber-200"
            >
              <AlertDescription>{{ localError }}</AlertDescription>
            </Alert>
            <Alert v-if="formError" variant="destructive">
              <AlertDescription class="break-all">{{ formError }}</AlertDescription>
            </Alert>

            <div class="grid gap-2">
              <Label for="pw-current">当前密码</Label>
              <Input
                id="pw-current"
                v-model="form.current"
                type="password"
                autocomplete="current-password"
                required
              />
            </div>

            <div class="grid gap-2">
              <Label for="pw-next">新密码</Label>
              <Input
                id="pw-next"
                v-model="form.next"
                type="password"
                autocomplete="new-password"
                required
                minlength="10"
              />
              <p class="text-xs text-muted-foreground">至少 10 位；服务端可能撤销其它会话</p>
            </div>

            <div class="grid gap-2">
              <Label for="pw-confirm">确认新密码</Label>
              <Input
                id="pw-confirm"
                v-model="form.confirm"
                type="password"
                autocomplete="new-password"
                required
              />
            </div>

            <div>
              <Button type="submit" :disabled="saving || !form.current || !form.next">
                <Loader2 v-if="saving" class="animate-spin" aria-hidden="true" />
                {{ saving ? '提交中…' : '更新密码' }}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="flex-row items-center justify-between space-y-0 border-b">
          <CardTitle class="text-base font-semibold">会话</CardTitle>
          <span class="text-xs text-muted-foreground">共 {{ sessionCount }} 个</span>
        </CardHeader>
        <CardContent class="pt-4">
          <LoadingBlock v-if="sessions.loading.value && !sessions.data.value" :rows="2" label="正在读取会话" />
          <ErrorState v-else-if="sessions.error.value" :error="sessions.error.value" @retry="sessions.reload()" />
          <EmptyState
            v-else-if="!sessionCount"
            title="没有活动会话"
            description="如果这里为空但你能看到本页，说明会话接口未返回记录；请联系管理员。"
          />
          <Table v-else>
            <TableHeader>
              <TableRow>
                <TableHead>会话</TableHead>
                <TableHead>创建</TableHead>
                <TableHead>最近活动</TableHead>
                <TableHead>来源</TableHead>
                <TableHead>操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="session in sessions.data.value" :key="session.id">
                <TableCell class="mono break-all">
                  {{ session.id }}
                  <Badge
                    v-if="isCurrent(session)"
                    variant="outline"
                    class="ml-1.5 border-transparent bg-brand text-brand-foreground"
                  >
                    当前
                  </Badge>
                </TableCell>
                <TableCell>{{ formatDateTime(session.created_at) }}</TableCell>
                <TableCell>
                  <span class="block">{{ formatDateTime(session.last_seen_at) }}</span>
                  <span class="block text-xs text-muted-foreground">{{ formatRelative(session.last_seen_at) }}</span>
                </TableCell>
                <TableCell>
                  <span class="block text-xs">{{ session.ip ?? '—' }}</span>
                  <span class="block break-all text-xs text-muted-foreground">{{ session.user_agent ?? '' }}</span>
                </TableCell>
                <TableCell>
                  <Button variant="destructive" size="sm" @click="revokeTarget = session">撤销</Button>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>
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
      <p v-if="revokeTarget && isCurrent(revokeTarget)" class="text-sm text-muted-foreground">
        这是当前会话：撤销后你会被退出登录。
      </p>
      <p v-else class="text-sm text-muted-foreground">该设备上的登录立即失效，不影响其它会话。</p>
    </ConfirmDialog>
  </div>
</template>
