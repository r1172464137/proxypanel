<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { adminApi } from '@/api/endpoints'
import type { RegistrationCode, ServerRecord } from '@/api/types'
import { usePagedList } from '@/lib/paged'
import { errorText, toApiError } from '@/lib/errors'
import { formatDate, formatDateTime, fromLocalInput, toLocalInput } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import AppModal from '@/components/AppModal.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import CopyField from '@/components/CopyField.vue'

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
</script>

<template>
  <div>
    <PageHeader title="服务器" description="机器档案与 Agent 注册入口。注册码一次性、15 分钟内有效。">
      <button type="button" class="btn btn--primary" @click="openCreate">新建服务器</button>
    </PageHeader>

    <ErrorState v-if="list.error.value" :error="list.error.value" @retry="list.refresh()" />
    <LoadingBlock v-else-if="list.loading.value && !list.items.value.length" label="正在读取服务器" />
    <div v-else class="card card__body--flush">
      <div v-if="!list.items.value.length" class="card__body">
        <EmptyState title="还没有服务器档案" description="登记服务器地址与到期信息后，可生成一次性注册码给该机器的 Agent。">
          <button type="button" class="btn btn--primary" @click="openCreate">新建服务器</button>
        </EmptyState>
      </div>
      <template v-else>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th>名称</th>
                <th>地址</th>
                <th class="hide-sm">地区</th>
                <th class="hide-sm">到期</th>
                <th class="hide-sm">成本</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="server in list.items.value" :key="server.id">
                <td>
                  <router-link :to="`/admin/servers/${server.id}/runtime`">{{ server.name }}</router-link>
                  <span class="cell-sub mono">{{ server.id }}</span>
                </td>
                <td class="mono">{{ server.address }}</td>
                <td class="hide-sm">{{ server.region || '—' }}</td>
                <td class="hide-sm">
                  <span :title="server.expires_at ?? ''">{{ formatDate(server.expires_at) }}</span>
                </td>
                <td class="hide-sm num">
                  <template v-if="server.cost_minor !== null && server.cost_minor !== undefined">
                    {{ server.cost_minor }} {{ server.currency ?? '' }}
                  </template>
                  <template v-else>—</template>
                </td>
                <td class="actions">
                  <button type="button" class="btn btn--sm" @click="generateCode(server)">注册码</button>
                  <button type="button" class="btn btn--sm" @click="openEdit(server)">编辑</button>
                  <button type="button" class="btn btn--sm btn--danger" @click="deleteTarget = server">归档</button>
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

    <AppModal :open="formOpen" :title="title" size="md" @close="formOpen = false">
      <div class="stack">
        <p v-if="formError" class="banner banner--danger" role="alert">{{ formError }}</p>
        <p v-for="error in formErrors" :key="error" class="banner banner--warn" role="alert">{{ error }}</p>
        <div class="grid grid--form">
          <div class="field">
            <label for="server-name">名称</label>
            <input id="server-name" v-model="form.name" class="input" required />
          </div>
          <div class="field">
            <label for="server-address">地址</label>
            <input id="server-address" v-model="form.address" class="input mono" placeholder="IP 或域名" required />
          </div>
          <div class="field">
            <label for="server-region">地区</label>
            <input id="server-region" v-model="form.region" class="input" />
          </div>
          <div class="field">
            <label for="server-expires">到期时间</label>
            <input id="server-expires" v-model="form.expires_at_local" class="input" type="datetime-local" />
            <p class="field__help">按浏览器本地时间输入，提交为 UTC</p>
          </div>
          <div class="field">
            <label for="server-cost">成本（最小货币单位）</label>
            <input id="server-cost" v-model="form.cost_minor" class="input" inputmode="numeric" placeholder="如 5000 = 50.00" />
          </div>
          <div class="field">
            <label for="server-currency">货币</label>
            <input id="server-currency" v-model="form.currency" class="input" maxlength="3" />
          </div>
        </div>
        <div class="field">
          <label for="server-notes">备注</label>
          <textarea id="server-notes" v-model="form.notes" class="textarea" style="min-height: 80px" />
        </div>
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
      title="归档服务器"
      confirm-label="归档"
      danger
      :pending="deleting"
      :error-text="deleteError"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    >
      <p>将归档服务器 <strong>{{ deleteTarget?.name }}</strong>。</p>
      <ul class="text-sm">
        <li>仍有关联 Agent/节点时服务端会拒绝并给出原因。</li>
        <li>归档保留历史记录，不物理删除账本。</li>
      </ul>
    </ConfirmDialog>

    <AppModal :open="codeOpen" title="一次性 Agent 注册码" size="sm" @close="codeOpen = false">
      <div class="stack">
        <p class="text-sm text-muted">
          服务器：<strong>{{ codeTarget?.name }}</strong> · 仅可使用一次，15 分钟内有效；关闭后不保证能再次查看。
        </p>
        <p v-if="codeLoading" class="text-sm"><span class="spinner" aria-hidden="true" /> 正在生成…</p>
        <p v-if="codeError" class="banner banner--danger" role="alert">{{ codeError }}</p>
        <template v-if="code">
          <CopyField :value="code.code" label="注册码" warning="注册码等同于 Agent 身份凭据；交付后不要留存明文。" />
          <p class="text-xs text-muted">过期时间：{{ formatDateTime(code.expires_at) }}</p>
        </template>
      </div>
      <template #footer>
        <button type="button" class="btn" @click="codeOpen = false">关闭</button>
      </template>
    </AppModal>
  </div>
</template>