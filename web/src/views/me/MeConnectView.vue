<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { meApi } from '@/api/endpoints'
import { useResource } from '@/lib/async'
import { memberSummary } from '@/lib/memberService'
import { errorText } from '@/lib/errors'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ErrorState from '@/components/ErrorState.vue'
import ClientImportGuide from '@/components/ClientImportGuide.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
const subscription = useResource(() => meApi.subscription())
const status = useResource(() => meApi.status())
const allowed = computed(() => !status.error.value && memberSummary(status.data.value).state === 'enabled')
const open = ref(false)
const pending = ref(false)
const error = ref('')
const result = ref('')
async function refresh() { await Promise.all([subscription.reload(), status.reload()]) }
onMounted(refresh)
async function rotate() {
  if (pending.value || result.value) return
  pending.value = true
  error.value = ''
  try {
    await meApi.rotateSubscriptionToken()
    // Remove the old credential immediately, even if the subsequent read fails.
    subscription.data.value = null
    result.value = '旧链接已失效。请使用下方重新读取的新链接更新客户端；连接凭据未改变。'
    await subscription.reload()
  } catch (caught) { error.value = `${errorText(caught)} 请先关闭确认框并刷新订阅资料核对，不要连续重置。` }
  finally { pending.value = false }
}
function showReset() { open.value = true; error.value = ''; result.value = '' }
</script>
<template>
  <div class="min-w-0 space-y-6">
    <PageHeader title="导入与订阅链接" description="选择真实可用的格式，复制、扫码或下载配置。链接仅供本人使用。">
      <Button class="min-h-11" variant="outline" :disabled="subscription.loading.value || pending" @click="refresh">刷新订阅资料</Button>
    </PageHeader>
    <p v-if="result" data-testid="subscription-rotation-result" role="status" class="text-sm">{{ result }}</p>
    <LoadingBlock v-if="subscription.loading.value || status.loading.value" label="正在读取导入资料" />
    <div v-else-if="subscription.error.value?.code === 'RECOVERY_MODE'" role="status" class="space-y-3">
      <p>恢复核对进行中，当前没有可只读获取的订阅链接。稍后重试；服务状态与用量仍可查看。</p>
      <Button class="min-h-11" variant="outline" @click="subscription.reload()">重试获取导入资料</Button>
    </div>
    <ErrorState v-else-if="subscription.error.value" :error="subscription.error.value" @retry="subscription.reload()" />
    <ErrorState v-else-if="status.error.value" :error="status.error.value" @retry="status.reload()" />
    <Card v-else id="link">
      <CardHeader><CardTitle>订阅链接与格式</CardTitle></CardHeader>
      <CardContent class="space-y-4">
        <p v-if="!allowed" role="status">{{ memberSummary(status.data.value).title }}。请先核对服务状态，或联系为你开通服务的管理员。</p>
        <ClientImportGuide :subscription="subscription.data.value" :allowed="allowed" detailed />
      </CardContent>
    </Card>
    <Card>
      <CardHeader><CardTitle>重置订阅链接</CardTitle></CardHeader>
      <CardContent class="space-y-4">
        <p class="text-sm">旧网址将立即失效，无法再下载配置；连接凭据不变，不等于立即切断现有连接。如需使泄露的节点凭据失效，请另行重置连接凭据。</p>
        <Button data-testid="subscription-reset-button" class="min-h-11" variant="destructive" :disabled="pending" @click="showReset">重置订阅链接</Button>
      </CardContent>
    </Card>
    <div class="flex flex-wrap gap-4 text-sm underline"><router-link class="inline-flex min-h-11 items-center" to="/me/subscription">返回我的服务</router-link><router-link class="inline-flex min-h-11 items-center" to="/me/usage">查看用量</router-link><router-link class="inline-flex min-h-11 items-center" to="/me/credentials">重置连接凭据</router-link></div>
    <ConfirmDialog :open="open" title="重置订阅链接" confirm-label="重置链接" danger :pending="pending" :confirm-disabled="!!result || !!error" :error-text="error" @cancel="open = false" @confirm="rotate">
      <p>旧订阅链接立即失效，客户端需要更新导入链接。连接凭据不会改变，也不等于立即断开现有连接。</p>
      <p v-if="result" role="status">{{ result }}</p>
    </ConfirmDialog>
  </div>
</template>
