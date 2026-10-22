<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { adminApi } from '@/api/endpoints'
import type { RegistrationCode, ServerRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText, toApiError } from '@/lib/errors'
import { formatDate, formatDateTime, fromLocalInput, toLocalInput } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import DataTable from '@/components/DataTable.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import CopyField from '@/components/CopyField.vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

const list = usePagedList<ServerRecord>((cursor) => adminApi.servers.list({ limit: 50, cursor }))

onMounted(() => void list.refresh())

interface ServerFormState {
  id: string | null
  version: number | null
  name: string
  address: string
  region: string
  notes: string
  expires_at_local: string
  cost_minor: string
  currency: string
}

function emptyForm(): ServerFormState {
  return { id: null, version: null, name: '', address: '', region: '', notes: '', expires_at_local: '', cost_minor: '', currency: 'CNY' }
}

const formOpen = ref(false)
const form = reactive<ServerFormState>(emptyForm())
const formError = ref('')
const saving = ref(false)
const formErrors = ref<string[]>([])

const deleteTarget = ref<ServerRecord | null>(null)
const deleting = ref(false)
const deleteError = ref('')

const codeOpen = ref(false)
const code = ref<RegistrationCode | null>(null)
const codeError = ref('')
const codeLoading = ref(false)
const codeTarget = ref<ServerRecord | null>(null)

const title = computed(() => (form.id ? `编辑服务器 · ${form.name}` : '新建服务器'))

function openCreate(): void {
  Object.assign(form, emptyForm())
  formErrors.value = []
  formError.value = ''
  formOpen.value = true
}

function openEdit(server: ServerRecord): void {
  Object.assign(form, {
    id: server.id,
    version: server.version,
    name: server.name,
    address: server.address,
    region: server.region ?? '',
    notes: server.notes ?? '',
    expires_at_local: toLocalInput(server.expires_at),
    cost_minor: server.cost_minor === null || server.cost_minor === undefined ? '' : String(server.cost_minor),
    currency: server.currency ?? 'CNY',
  })
  formErrors.value = []
  formError.value = ''
  formOpen.value = true
}

function validate(): string[] {
  const errors: string[] = []
  if (!form.name.trim()) errors.push('名称必填')
  if (!form.address.trim()) errors.push('地址必填')
  if (form.cost_minor.trim()) {
    const cost = Number(form.cost_minor)
    if (!Number.isInteger(cost) || cost < 0) errors.push('成本必须是非负整数（最小货币单位）')
  }
  if (form.currency.trim().length !== 3) errors.push('货币代码必须是 3 位（如 CNY）')
  return errors
}

async function save(): Promise<void> {
  formErrors.value = validate()
  formError.value = ''
  if (formErrors.value.length) return
  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      address: form.address.trim(),
      region: form.region.trim() || null,
      notes: form.notes.trim() || null,
      expires_at: fromLocalInput(form.expires_at_local),
      cost_minor: form.cost_minor.trim() ? Number(form.cost_minor) : null,
      currency: form.cost_minor.trim() ? form.currency.trim().toUpperCase() : null,
    }
    if (form.id && form.version !== null) {
      await adminApi.servers.update(form.id, { ...payload, version: form.version })
    } else {
      await adminApi.servers.create(payload)
    }
    formOpen.value = false
    await list.refresh()
  } catch (caught) {
    formError.value = errorText(caught)
  } finally {
    saving.value = false
  }
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  deleting.value = true
  deleteError.value = ''
  try {
    await adminApi.servers.remove(target.id, target.version)
    deleteTarget.value = null
    await list.refresh()
  } catch (caught) {
    deleteError.value = toApiError(caught).message
  } finally {
    deleting.value = false
  }
}

async function generateCode(server: ServerRecord): Promise<void> {
  codeTarget.value = server
  code.value = null
  codeError.value = ''
  codeOpen.value = true
  codeLoading.value = true
  try {
    code.value = await adminApi.servers.registrationCode(server.id)
  } catch (caught) {
    codeError.value = errorText(caught)
  } finally {
    codeLoading.value = false
  }
}

const columns = [
  { label: '名称', width: 'w-56' },
  { label: '地址' },
  { label: '地区', width: 'w-28', hideOnMobile: true },
  { label: '到期', width: 'w-36', hideOnMobile: true },
  { label: '成本', width: 'w-32', hideOnMobile: true, numeric: true },
  { label: '操作', width: 'w-56' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="服务器" description="机器档案与 Agent 注册入口。注册码一次性、15 分钟内有效。">
      <Button @click="openCreate">新建服务器</Button>
    </PageHeader>

    <DataTable
      :list="list"
      :columns="columns"
      empty-title="还没有服务器档案"
      empty-description="登记服务器地址与到期信息后，可生成一次性注册码给该机器的 Agent。"
      loading-label="正在读取服务器"
      :count-label="`本页 ${list.items.value.length} 台`"
    >
      <template #row="{ item, columnIndex }">
        <template v-if="columnIndex === 0">
          <router-link :to="`/admin/servers/${item.id}/runtime`" class="font-medium hover:underline">{{ item.name }}</router-link>
          <span class="mt-0.5 block font-mono text-xs text-muted-foreground">{{ item.id }}</span>
        </template>
        <template v-else-if="columnIndex === 1">
          <span class="font-mono text-xs">{{ item.address }}</span>
        </template>
        <template v-else-if="columnIndex === 2">{{ item.region || '—' }}</template>
        <template v-else-if="columnIndex === 3">
          <span :title="item.expires_at ?? ''">{{ formatDate(item.expires_at) }}</span>
        </template>
        <template v-else-if="columnIndex === 4">
          <template v-if="item.cost_minor !== null && item.cost_minor !== undefined">
            {{ item.cost_minor }} {{ item.currency ?? '' }}
          </template>
          <template v-else>—</template>
        </template>
        <template v-else>
          <div class="flex flex-wrap items-center gap-1.5">
            <Button size="sm" variant="outline" @click="generateCode(item)">注册码</Button>
            <Button size="sm" variant="outline" @click="openEdit(item)">编辑</Button>
            <Button size="sm" variant="destructive" @click="deleteTarget = item">归档</Button>
          </div>
        </template>
      </template>
      <template #empty-action>
        <Button @click="openCreate">新建服务器</Button>
      </template>
    </DataTable>

    <AppModal :open="formOpen" :title="title" size="md" @close="formOpen = false">
      <div class="space-y-4">
        <Alert v-if="formError" variant="destructive" role="alert">
          <AlertTitle>保存失败</AlertTitle>
          <AlertDescription class="break-all">{{ formError }}</AlertDescription>
        </Alert>
        <Alert v-for="error in formErrors" :key="error" variant="destructive" role="alert">
          <AlertDescription>{{ error }}</AlertDescription>
        </Alert>

        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label for="server-name">名称</Label>
            <Input id="server-name" v-model="form.name" required />
          </div>
          <div class="space-y-1.5">
            <Label for="server-address">地址</Label>
            <Input id="server-address" v-model="form.address" class="font-mono" placeholder="IP 或域名" required />
          </div>
          <div class="space-y-1.5">
            <Label for="server-region">地区</Label>
            <Input id="server-region" v-model="form.region" />
          </div>
          <div class="space-y-1.5">
            <Label for="server-expires">到期时间</Label>
            <Input id="server-expires" v-model="form.expires_at_local" type="datetime-local" />
            <p class="text-xs text-muted-foreground">按浏览器本地时间输入，提交为 UTC</p>
          </div>
          <div class="space-y-1.5">
            <Label for="server-cost">成本（元）</Label>
            <Input id="server-cost" v-model="form.cost_minor" inputmode="numeric" placeholder="如 5000 = 50.00" />
          </div>
          <div class="space-y-1.5">
            <Label for="server-currency">货币</Label>
            <Input id="server-currency" v-model="form.currency" maxlength="3" />
          </div>
        </div>

        <div class="space-y-1.5">
          <Label for="server-notes">备注</Label>
          <Textarea id="server-notes" v-model="form.notes" class="min-h-20" />
        </div>
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
      title="归档服务器"
      confirm-label="归档"
      danger
      :pending="deleting"
      :error-text="deleteError"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p>将归档服务器 <strong>{{ deleteTarget?.name }}</strong>。</p>
      <ul class="list-disc space-y-1 pl-5 text-sm">
        <li>仍有关联 Agent/节点时服务端会拒绝并给出原因。</li>
        <li>归档保留历史记录，不物理删除账本。</li>
      </ul>
    </ConfirmDialog>

    <AppModal :open="codeOpen" title="一次性 Agent 注册码" size="sm" @close="codeOpen = false">
      <div class="space-y-4">
        <p class="text-sm text-muted-foreground">
          服务器：<strong class="text-foreground">{{ codeTarget?.name }}</strong> · 仅可使用一次，15 分钟内有效；关闭后不保证能再次查看。
        </p>
        <p v-if="codeLoading" class="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 class="animate-spin" aria-hidden="true" /> 正在生成…
        </p>
        <Alert v-if="codeError" variant="destructive" role="alert">
          <AlertDescription class="break-all">{{ codeError }}</AlertDescription>
        </Alert>
        <template v-if="code">
          <CopyField :value="code.code" label="注册码" warning="注册码等同于 Agent 身份凭据；交付后不要留存明文。" />
          <p class="text-xs text-muted-foreground">过期时间：{{ formatDateTime(code.expires_at) }}</p>
        </template>
      </div>
      <template #footer>
        <Button variant="outline" @click="codeOpen = false">关闭</Button>
      </template>
    </AppModal>
  </div>
</template>
