<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { MeSubscription, SubscriptionFormat } from '@/api/types'
import { MEMBER_CLIENTS, formatAvailability, memberSubscriptionUrl } from '@/lib/clientImport'
import CopyField from '@/components/CopyField.vue'
import QrPanel from '@/components/QrPanel.vue'
import { Button } from '@/components/ui/button'

const props = withDefaults(defineProps<{ subscription: MeSubscription | null; allowed: boolean; detailed?: boolean }>(), { detailed: false })
const format = ref<SubscriptionFormat>('v2rayn')
watch(() => props.subscription, data => {
  const first = data?.formats.find(item => formatAvailability(data, item).available)
  if (first) format.value = first
}, { immediate: true })
const client = computed(() => MEMBER_CLIENTS.find(item => item.format === format.value)!)
const availability = computed(() => formatAvailability(props.subscription, format.value))
const url = computed(() => props.allowed ? memberSubscriptionUrl(props.subscription, format.value, window.location.origin) : '')
const checking = ref(false)
const message = ref('')
let generation = 0
onBeforeUnmount(() => { generation++ })
watch([url, () => props.subscription], () => { generation++; checking.value = false; message.value = '' })
async function retrieve(download = false) {
  if (!url.value || checking.value) return
  const target = url.value
  const selected = format.value
  const current = ++generation
  checking.value = true
  message.value = ''
  try {
    const response = await fetch(target, { credentials: 'omit', referrerPolicy: 'no-referrer', headers: { Accept: 'text/plain, application/json, application/yaml' } })
    if (!response.ok) throw new Error(response.status === 422 ? '此格式无法表达当前节点配置，请更换客户端或联系管理员。' : response.status === 403 ? '当前授权不允许获取配置，请刷新服务状态。' : response.status === 409 ? '配置暂不可获取，请稍后重试或联系管理员。' : '暂时无法获取配置，请稍后重试。')
    const body = await response.text()
    if (!body.trim() || /^\s*</.test(body) || response.headers.get('content-type')?.includes('text/html')) throw new Error('未获取到有效配置，请联系管理员核对服务地址。')
    if (current !== generation) return
    message.value = '配置已取回；未验证真实连接，请在客户端选择节点并检查实际访问。'
    if (download) {
      const objectUrl = URL.createObjectURL(new Blob([body], { type: 'application/octet-stream' }))
      const anchor = document.createElement('a')
      anchor.href = objectUrl
      anchor.download = `my-service.${selected === 'mihomo' ? 'yaml' : selected === 'v2rayn' ? 'txt' : 'json'}`
      anchor.click()
      window.setTimeout(() => URL.revokeObjectURL(objectUrl), 1000)
      message.value = '配置已取回并发起下载；未验证真实连接。配置包含个人连接信息，请勿转发。'
    }
  } catch (error) {
    if (current === generation) message.value = `取回失败：${error instanceof TypeError ? '网络连接失败，请稍后重试。' : error instanceof Error ? error.message : '请重试。'}未验证真实连接。`
  } finally { if (current === generation) checking.value = false }
}
</script>

<template>
  <div class="min-w-0 space-y-4">
    <div class="space-y-2">
      <label for="member-client" class="text-sm font-medium">你使用的客户端</label>
      <select id="member-client" v-model="format" data-testid="client-import-select" class="block min-h-11 w-full rounded-md border border-border bg-background p-2 text-sm">
        <option v-for="item in MEMBER_CLIENTS" :key="item.format" :value="item.format">{{ item.name }}</option>
      </select>
      <p role="status" class="text-sm text-muted-foreground">{{ availability.message }}</p>
    </div>
    <template v-if="url">
      <CopyField data-testid="subscription-link-value" :value="url" label="个人订阅链接" success-label="已复制订阅链接" mask warning="仅供本人使用。复制不会重置链接，也不会更改连接凭据。" />
      <details v-if="detailed" id="qrcode" data-testid="import-qrcode-panel"><summary class="flex min-h-11 cursor-pointer items-center text-sm">显示二维码（仅供本人使用）</summary><QrPanel :value="url" /></details>
    </template>
    <div class="flex flex-wrap gap-2">
      <Button data-testid="import-check-button" class="min-h-11" variant="outline" :disabled="!url || checking" @click="retrieve()">{{ checking ? '正在获取配置…' : '检查配置能否获取' }}</Button>
      <Button v-if="url" data-testid="import-download-button" class="min-h-11" variant="outline" :disabled="checking" @click="retrieve(true)">下载配置文件</Button>
    </div>
    <p v-if="message" data-testid="import-result-message" role="status" class="text-sm">{{ message }}</p>
    <ol class="list-decimal space-y-2 pl-5 text-sm">
      <li>安装并打开所选客户端，核对客户端版本是否支持当前格式。</li>
      <li>{{ client.instruction }}</li>
      <li>更新订阅，在客户端选择同名节点，然后开启连接。</li>
      <li>在客户端查看日志并检查实际访问。取回配置、复制或扫码均未验证真实连接。</li>
    </ol>
    <p class="text-xs text-muted-foreground">手动导入指引不代表兼容认证，不提供未经验证的一键唤起。</p>
    <router-link v-if="!detailed" to="/me/connect" class="inline-flex min-h-11 items-center text-sm underline">二维码、格式与订阅链接管理</router-link>
  </div>
</template>
