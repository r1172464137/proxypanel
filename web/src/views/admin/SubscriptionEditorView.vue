<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import { allPages } from '@/lib/allPages'
import type { BatchOperation, NodeGroupRecord, SubscriptionRecord, TemplateRecord } from '@/api/types'
import { buildSubscriptionWrite, draftFromSubscription, emptySubscriptionDraft, type SubscriptionDraft } from '@/lib/subscriptionDraft'
import { errorText, isPreviewRequired } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ImpactPreviewDialog from '@/components/ImpactPreviewDialog.vue'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Loader2 } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()

const isEdit = computed(() => route.name === 'admin-subscription-edit' && typeof route.params.id === 'string')
const draft = reactive<SubscriptionDraft>(emptySubscriptionDraft())
const record = ref<SubscriptionRecord | null>(null)
const groups = ref<NodeGroupRecord[]>([])
const templates = ref<TemplateRecord[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const referenceError = ref<unknown>(null)
const saving = ref(false)
const saveError = ref('')

const impact = reactive<{ open: boolean; operation: BatchOperation; values: Record<string, unknown> }>({
  open: false,
  operation: 'update_subscription',
  values: {},
})

const build = computed(() => buildSubscriptionWrite(draft))
const zoneSuggestions = ['Asia/Shanghai', 'Asia/Tokyo', 'Asia/Singapore', 'Europe/Berlin', 'America/New_York', 'UTC']

async function load(): Promise<void> {
  loading.value = true
  loadError.value = null
  try {
    const [groupPage, templatePage] = await Promise.all([
      allPages(cursor => adminApi.nodeGroups.list({ limit: 200, cursor })).then(items => ({ items })),
      allPages(cursor => adminApi.templates.list({ limit: 200, cursor })).then(items => ({ items })),
    ])
    groups.value = groupPage.items
    templates.value = templatePage.items
  } catch (caught) {
    referenceError.value = caught
    groups.value = []
    templates.value = []
  }
  if (isEdit.value) {
    try {
      const sub = await adminApi.subscriptions.get(String(route.params.id))
      record.value = sub
      Object.assign(draft, draftFromSubscription(sub))
    } catch (caught) {
      loadError.value = caught
    }
  }
  loading.value = false
}

onMounted(() => void load())

function toggleGroup(id: string): void {
  const index = draft.node_group_ids.indexOf(id)
  if (index >= 0) draft.node_group_ids.splice(index, 1)
  else draft.node_group_ids.push(id)
}

async function save(): Promise<void> {
  saveError.value = ''
  if (!build.value.payload) {
    saveError.value = build.value.errors.join('；')
    return
  }
  saving.value = true
  try {
    if (isEdit.value && record.value) {
      const payload = { ...build.value.payload, version: record.value.version }
      try {
        await adminApi.subscriptions.update(record.value.id, payload)
        pushToast('success', '订阅规则已保存', '规则已在面板生效；节点分发为异步，请在任务与同步结果中确认回执。')
        await router.push('/admin/subscriptions')
      } catch (caught) {
        if (isPreviewRequired(caught)) {
          const patch = { ...build.value.payload }
          impact.values = { id: record.value.id, version: record.value.version, patch }
          impact.open = true
          return
        }
        throw caught
      }
    } else {
      await adminApi.subscriptions.create(build.value.payload)
      pushToast('success', '订阅已创建', '下一步：为成员绑定该订阅，或生成本订阅的邀请码。')
      await router.push('/admin/subscriptions')
    }
  } catch (caught) {
    saveError.value = errorText(caught)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      :title="isEdit ? '编辑服务方案' : '新建服务方案'"
      description="配额、周期与访问开关分别生效：配额立即影响当前周期；anchor_day / timezone 仅对下一周期生效，当前周期与已用量保留。"
    >
      <Button as-child variant="outline">
        <router-link to="/admin/subscriptions">返回列表</router-link>
      </Button>
      <Button :disabled="saving" @click="save">
        <Loader2 v-if="saving" class="animate-spin" aria-hidden="true" />
        {{ saving ? '保存中…' : '保存服务方案' }}
      </Button>
    </PageHeader>

    <LoadingBlock v-if="loading" label="正在读取订阅" />
    <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />
    <div v-else class="space-y-4">
      <Alert v-if="saveError" variant="destructive">
        <AlertTitle>保存失败</AlertTitle>
        <AlertDescription class="break-all">{{ saveError }}</AlertDescription>
      </Alert>
      <ErrorState v-if="referenceError" :error="referenceError" @retry="load" />
      <Alert v-if="record">
        <AlertDescription>
          订阅 ID <span class="font-mono">{{ record.id }}</span> · 版本 {{ record.version }}
          <template v-if="record.updated_at"> · 更新于 {{ formatDateTime(record.updated_at) }}</template>
        </AlertDescription>
      </Alert>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">基本信息</CardTitle>
        </CardHeader>
        <CardContent class="space-y-4">
          <div class="grid gap-4 sm:grid-cols-2">
            <div class="space-y-1.5">
              <Label for="sub-name">名称</Label>
              <Input id="sub-name" v-model="draft.name" required />
            </div>
            <div class="space-y-1.5">
              <Label for="sub-template">客户端模板</Label>
              <select
                id="sub-template"
                v-model="draft.template_id"
                class="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              >
                <option value="">不指定</option>
                <option v-for="template in templates" :key="template.id" :value="template.id">
                  {{ template.name }}（{{ template.format }}）
                </option>
              </select>
            </div>
          </div>
          <div class="space-y-1.5">
            <Label for="sub-desc">说明</Label>
            <Textarea id="sub-desc" v-model="draft.description" class="min-h-[70px]" />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">节点组与配额</CardTitle>
        </CardHeader>
        <CardContent class="space-y-4">
          <div class="space-y-2">
            <Label>授权节点组（{{ draft.node_group_ids.length }}）</Label>
            <div class="max-h-60 space-y-1 overflow-y-auto rounded-md border border-border p-3">
              <div
                v-for="group in groups"
                :key="group.id"
                class="flex cursor-pointer items-start gap-2 rounded-sm px-1 py-1.5 hover:bg-muted/60"
                @click="toggleGroup(group.id)"
              >
                <Checkbox
                  class="mt-0.5"
                  :model-value="draft.node_group_ids.includes(group.id)"
                  @update:model-value="toggleGroup(group.id)"
                  @click.stop
                />
                <span class="select-none text-sm">
                  {{ group.name }}
                  <span class="block text-xs text-muted-foreground">{{ group.node_ids.length }} 个节点</span>
                </span>
              </div>
              <p v-if="!groups.length" class="text-sm text-muted-foreground">还没有节点组，请先创建节点组。</p>
            </div>
            <p class="text-xs text-muted-foreground">多组引用时输出按节点去重；节点组变更影响授权需预览。</p>
          </div>

          <div class="grid gap-4 sm:grid-cols-2">
            <div class="space-y-1.5">
              <Label for="sub-quota">每人配额（GiB）</Label>
              <Input
                id="sub-quota"
                v-model="draft.quota_gib"
                inputmode="decimal"
                :disabled="draft.unlimited"
                placeholder="如 100"
              />
              <div class="flex items-center gap-2">
                <Checkbox
                  id="sub-unlimited"
                  :model-value="draft.unlimited"
                  @update:model-value="(value) => (draft.unlimited = value === true)"
                />
                <Label for="sub-unlimited" class="font-normal leading-snug">
                  不限量（quota_bytes = null；仍受节点与规则限制）
                </Label>
              </div>
            </div>
            <div class="space-y-1.5">
              <Label for="sub-valid-days">默认有效期（天）</Label>
              <Input id="sub-valid-days" v-model="draft.default_valid_days" inputmode="numeric" placeholder="留空表示不设置" />
              <p class="text-xs text-muted-foreground">仅用于新绑定默认值；修改它不会改变已有成员绑定。</p>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">周期与时区</CardTitle>
        </CardHeader>
        <CardContent class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-1.5">
            <Label for="sub-anchor">周期起始日（1–31）</Label>
            <Input id="sub-anchor" v-model="draft.anchor_day" inputmode="numeric" />
            <p class="text-xs text-muted-foreground">每月该日 00:00 起算；月末自动裁剪（31 日在 2 月落到 28/29 日）。</p>
          </div>
          <div class="space-y-1.5">
            <Label for="sub-tz">时区</Label>
            <Input id="sub-tz" v-model="draft.timezone" class="font-mono" list="tz-list" />
            <datalist id="tz-list">
              <option v-for="zone in zoneSuggestions" :key="zone" :value="zone" />
            </datalist>
            <p class="text-xs text-muted-foreground">必须显式配置，不从浏览器或宿主时区推断。修改只对下一周期生效。</p>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">访问与绑定开关</CardTitle>
        </CardHeader>
        <CardContent class="space-y-4">
          <div class="space-y-1.5">
            <div class="flex items-center gap-2">
              <Checkbox
                id="sub-allow-new-bindings"
                :model-value="draft.allow_new_bindings"
                @update:model-value="(value) => (draft.allow_new_bindings = value === true)"
              />
              <Label for="sub-allow-new-bindings" class="font-normal leading-snug">
                允许新成员绑定 / 切换进入该订阅
              </Label>
            </div>
          </div>
          <div class="space-y-1.5">
            <div class="flex items-center gap-2">
              <Checkbox
                id="sub-access-enabled"
                :model-value="draft.access_enabled"
                @update:model-value="(value) => (draft.access_enabled = value === true)"
              />
              <Label for="sub-access-enabled" class="font-normal leading-snug">允许现有成员访问</Label>
            </div>
            <p class="text-xs text-muted-foreground">
              关闭后将暂停此方案的访问权限。节点异步同步；同一节点上的连接可能短暂中断，请到任务页核对实际结果。
            </p>
          </div>
          <div class="space-y-1.5">
            <div class="flex items-center gap-2">
              <Checkbox
                id="sub-archived"
                :model-value="draft.archived"
                @update:model-value="(value) => (draft.archived = value === true)"
              />
              <Label for="sub-archived" class="font-normal leading-snug">
                归档订阅（不可再绑定；仍有绑定成员时需先迁移或解除）
              </Label>
            </div>
            <p class="text-xs text-muted-foreground">三个开关互相独立：账户状态、代理访问、订阅归档不可合并。</p>
          </div>
        </CardContent>
      </Card>

      <Card v-if="build.errors.length || build.warnings.length">
        <CardHeader>
          <CardTitle class="text-base">表单校验</CardTitle>
        </CardHeader>
        <CardContent class="space-y-3">
          <Alert v-if="build.errors.length" variant="destructive">
            <AlertTitle>阻止保存</AlertTitle>
            <AlertDescription>
              <ul class="list-disc space-y-1 pl-4">
                <li v-for="error in build.errors" :key="error">{{ error }}</li>
              </ul>
            </AlertDescription>
          </Alert>
          <Alert v-if="build.warnings.length">
            <AlertTitle>保存前请确认</AlertTitle>
            <AlertDescription>
              <ul class="list-disc space-y-1 pl-4">
                <li v-for="warning in build.warnings" :key="warning">{{ warning }}</li>
              </ul>
            </AlertDescription>
          </Alert>
        </CardContent>
      </Card>
    </div>

    <ImpactPreviewDialog
      :open="impact.open"
      :operation="impact.operation"
      :member-ids="[]"
      :values="impact.values"
      title="订阅规则影响预览"
      description="该订阅已有绑定成员，规则变更需服务端预览影响范围后提交。预览包含每人前值/后值、超额与到期预测、受影响节点数。"
      @close="impact.open = false"
    />
  </div>
</template>
