<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { meApi } from '@/api/endpoints'
import { useResource } from '@/lib/async'
import { credentialRotationRequest } from '@/lib/clientImport'
import { errorText } from '@/lib/errors'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
const nodes = useResource(() => meApi.nodes())
const open = ref(false)
const scope = ref<'single_node' | 'all_nodes'>('single_node')
const nodeId = ref('')
const allConfirmed = ref(false)
const pending = ref(false)
const error = ref('')
const done = ref('')
onMounted(() => nodes.reload())
function showReset() {
  scope.value = 'single_node'; nodeId.value = ''; allConfirmed.value = false
  error.value = ''; done.value = ''; open.value = true
}
async function rotate() {
  if (pending.value || done.value || error.value) return
  let request
  try { request = credentialRotationRequest(scope.value, nodeId.value, allConfirmed.value) }
  catch (caught) { error.value = errorText(caught); return }
  pending.value = true
  try {
    await meApi.rotateCredentials(request)
    const name = nodes.data.value?.items.find(node => (node.id ?? node.node_id) === nodeId.value)?.name ?? '指定节点'
    done.value = `已提交重置${scope.value === 'all_nodes' ? '全部节点' : `「${name}」`}的连接凭据。已保存，正在同步；旧凭据失效以节点更新为准，相关客户端需重新导入。未验证真实连接。`
  } catch (caught) { error.value = `${errorText(caught)} 未确认完成，请先核对客户端配置与节点更新，不要连续重置。` }
  finally { pending.value = false }
}
</script>
<template>
  <div class="min-w-0 space-y-6">
    <PageHeader title="连接凭据" description="连接凭据与订阅链接分开管理。默认只重置指定节点。">
      <Button class="min-h-11" variant="outline" :disabled="nodes.loading.value" @click="nodes.reload()">刷新授权节点</Button>
    </PageHeader>
    <p v-if="done" data-testid="credentials-rotation-result" role="status" class="text-sm">{{ done }}</p>
    <Card>
      <CardHeader><CardTitle>重置连接凭据</CardTitle></CardHeader>
      <CardContent class="space-y-4">
        <p class="text-sm">这会改变实际连接信息，所选节点的旧凭据将失效，客户端需更新订阅并等待节点同步。共享节点可能短暂中断。不会恢复任何永久失效的旧凭据。</p>
        <Button class="min-h-11" data-testid="credentials-reset-button" variant="destructive" @click="showReset">重置连接凭据</Button>
        <p class="text-sm">只想更换下载地址？<router-link class="inline-flex min-h-11 items-center underline" to="/me/connect">前往订阅链接页重置订阅链接</router-link>（不改变实际连接凭据）。</p>
      </CardContent>
    </Card>
    <Card>
      <CardHeader><CardTitle>授权节点</CardTitle></CardHeader>
      <CardContent class="space-y-3">
        <LoadingBlock v-if="nodes.loading.value" label="正在读取节点" />
        <ErrorState v-else-if="nodes.error.value" :error="nodes.error.value" @retry="nodes.reload()" />
        <ul v-else-if="nodes.data.value?.items.length" class="space-y-2 text-sm"><li v-for="(node, index) in nodes.data.value.items" :key="node.id ?? node.node_id ?? index" class="break-words">{{ node.name ?? '未命名节点' }} · {{ node.protocol ?? '协议未提供' }}</li></ul>
        <p v-else>当前没有授权节点，请联系为你开通服务的管理员。</p>
        <p v-if="nodes.data.value?.next_cursor" class="text-sm">这里只展示部分授权节点；全量重置会包含未列出的节点，请谨慎确认。</p>
      </CardContent>
    </Card>
    <ConfirmDialog :open="open" title="重置连接凭据" confirm-label="重置连接凭据" danger :pending="pending" :confirm-disabled="!!done || !!error || (scope === 'single_node' ? !nodeId : !allConfirmed)" :error-text="error" @cancel="open = false" @confirm="rotate">
      <p>所选节点的旧凭据将失效，相关客户端需更新订阅并等待节点同步，不能用提交成功判断现有连接已经断开。</p>
      <fieldset :disabled="pending || !!done" class="space-y-2">
        <legend class="text-sm font-medium">重置范围</legend>
        <label class="flex min-h-11 items-center gap-2"><input v-model="scope" type="radio" value="single_node" @change="allConfirmed = false" />仅指定节点</label>
        <label class="flex min-h-11 items-center gap-2"><input v-model="scope" type="radio" value="all_nodes" />全部节点</label>
        <div v-if="scope === 'single_node'" class="space-y-2">
          <label for="cred-node">节点</label>
          <select id="cred-node" v-model="nodeId" class="min-h-11 w-full rounded-md border border-input bg-background p-2">
            <option value="">选择节点</option>
            <option v-for="node in nodes.data.value?.items ?? []" :key="node.id ?? node.node_id" :value="node.id ?? node.node_id" :disabled="!node.id && !node.node_id">{{ node.name ?? '未命名节点' }}</option>
          </select>
        </div>
        <label v-else class="flex min-h-11 items-center gap-2"><input v-model="allConfirmed" type="checkbox" />我确认全部节点的旧连接凭据将失效，所有相关客户端都需要更新订阅。</label>
      </fieldset>
      <p v-if="done" role="status">{{ done }}</p>
    </ConfirmDialog>
  </div>
</template>
