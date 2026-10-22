<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { adminApi } from '@/api/endpoints'
import type { BatchOperation, NodeGroupRecord, SubscriptionRecord, TemplateRecord } from '@/api/types'
import { buildSubscriptionWrite, draftFromSubscription, emptySubscriptionDraft, type SubscriptionDraft } from '@/lib/subscriptionDraft'
import { errorText, isPreviewRequired } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { pushToast } from '@/lib/toast'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ImpactPreviewDialog from '@/components/ImpactPreviewDialog.vue'

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
      adminApi.nodeGroups.list({ limit: 200 }),
      adminApi.templates.list({ limit: 200 }),
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
  <div>
    <PageHeader
      :title="isEdit ? '编辑订阅' : '新建订阅'"
      description="配额、周期与访问开关分别生效：配额立即影响当前周期；anchor_day / timezone 仅对下一周期生效，当前周期与已用量保留。"
    >
      <router-link class="btn btn--sm" to="/admin/subscriptions">返回列表</router-link>
      <button type="button" class="btn btn--primary" :disabled="saving" @click="save">
        <span v-if="saving" class="spinner" aria-hidden="true" />
        {{ saving ? '保存中…' : '保存订阅' }}
      </button>
    </PageHeader>

    <LoadingBlock v-if="loading" label="正在读取订阅" />
    <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />
    <div v-else class="stack">
      <p v-if="saveError" class="banner banner--danger" role="alert">{{ saveError }}</p>
      <ErrorState v-if="referenceError" :error="referenceError" @retry="load" />
      <div v-if="record" class="banner banner--info">
        订阅 ID <span class="mono">{{ record.id }}</span> · 版本 {{ record.version }}
        <template v-if="record.updated_at"> · 更新于 {{ formatDateTime(record.updated_at) }}</template>
      </div>

      <section class="card">
        <div class="card__header"><h2>基本信息</h2></div>
        <div class="card__body grid grid--form">
          <div class="field">
            <label for="sub-name">名称</label>
            <input id="sub-name" v-model="draft.name" class="input" required />
          </div>
          <div class="field">
            <label for="sub-template">客户端模板</label>
            <select id="sub-template" v-model="draft.template_id" class="select">
              <option value="">不指定</option>
              <option v-for="template in templates" :key="template.id" :value="template.id">
                {{ template.name }}（{{ template.format }}）
              </option>
            </select>
          </div>
        </div>
        <div class="card__body" style="padding-top: 0">
          <div class="field">
            <label for="sub-desc">说明</label>
            <textarea id="sub-desc" v-model="draft.description" class="textarea" style="min-height: 70px" />
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card__header"><h2>节点组与配额</h2></div>
        <div class="card__body stack">
          <div class="field">
            <label>授权节点组（{{ draft.node_group_ids.length }}）</label>
            <div class="picker">
              <label v-for="group in groups" :key="group.id" class="checkbox">
                <input type="checkbox" :checked="draft.node_group_ids.includes(group.id)" @change="toggleGroup(group.id)" />
                <span>
                  {{ group.name }}
                  <span class="text-xs text-muted">{{ group.node_ids.length }} 个节点</span>
                </span>
              </label>
              <p v-if="!groups.length" class="text-sm text-muted">还没有节点组，请先创建节点组。</p>
            </div>
            <p class="field__help">多组引用时输出按节点去重；节点组变更影响授权需预览。</p>
          </div>

          <div class="grid grid--form">
            <div class="field">
              <label for="sub-quota">每人配额（GiB）</label>
              <input
                id="sub-quota"
                v-model="draft.quota_gib"
                class="input"
                inputmode="decimal"
                :disabled="draft.unlimited"
                placeholder="如 100"
              />
              <label class="checkbox">
                <input v-model="draft.unlimited" type="checkbox" />
                不限量（quota_bytes = null；仍受节点与规则限制）
              </label>
            </div>
            <div class="field">
              <label for="sub-valid-days">默认有效期（天）</label>
              <input id="sub-valid-days" v-model="draft.default_valid_days" class="input" inputmode="numeric" placeholder="留空表示不设置" />
              <p class="field__help">仅用于新绑定默认值；修改它不会改变已有成员绑定。</p>
            </div>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card__header"><h2>周期与时区</h2></div>
        <div class="card__body grid grid--form">
          <div class="field">
            <label for="sub-anchor">周期起始日（1–31）</label>
            <input id="sub-anchor" v-model="draft.anchor_day" class="input" inputmode="numeric" />
            <p class="field__help">每月该日 00:00 起算；月末自动裁剪（31 日在 2 月落到 28/29 日）。</p>
          </div>
          <div class="field">
            <label for="sub-tz">时区</label>
            <input id="sub-tz" v-model="draft.timezone" class="input mono" list="tz-list" />
            <datalist id="tz-list">
              <option v-for="zone in zoneSuggestions" :key="zone" :value="zone" />
            </datalist>
            <p class="field__help">必须显式配置，不从浏览器或宿主时区推断。修改只对下一周期生效。</p>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card__header"><h2>访问与绑定开关</h2></div>
        <div class="card__body stack">
          <label class="checkbox">
            <input v-model="draft.allow_new_bindings" type="checkbox" />
            允许新成员绑定 / 切换进入该订阅
          </label>
          <label class="checkbox">
            <input v-model="draft.access_enabled" type="checkbox" />
            允许现有成员访问
          </label>
          <p class="field__help">
            关掉后，绑定了这个订阅的人立刻失去代理权限，正在用的连接会断开。
          </p>
          <label class="checkbox">
            <input v-model="draft.archived" type="checkbox" />
            归档订阅（不可再绑定；仍有绑定成员时需先迁移或解除）
          </label>
          <p class="text-xs text-muted">三个开关互相独立：账户状态、代理访问、订阅归档不可合并。</p>
        </div>
      </section>

      <section v-if="build.errors.length || build.warnings.length" class="card">
        <div class="card__header"><h2>表单校验</h2></div>
        <div class="card__body stack">
          <div v-if="build.errors.length" class="banner banner--danger">
            <div>
              <div class="banner__title">阻止保存</div>
              <ul><li v-for="error in build.errors" :key="error">{{ error }}</li></ul>
            </div>
          </div>
          <div v-if="build.warnings.length" class="banner banner--warn">
            <div>
              <div class="banner__title">保存前请确认</div>
              <ul><li v-for="warning in build.warnings" :key="warning">{{ warning }}</li></ul>
            </div>
          </div>
        </div>
      </section>
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

<style scoped>
.picker {
  display: grid;
  gap: var(--space-2);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: var(--space-3);
  max-height: 240px;
  overflow-y: auto;
}
</style>