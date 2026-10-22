<script setup lang="ts">
import { ref, watch } from 'vue'
import QRCode from 'qrcode'
import { Card } from '@/components/ui/card'

const props = defineProps<{ value: string; caption?: string }>()
const dataUrl = ref('')
const error = ref('')

async function render(): Promise<void> {
  error.value = ''
  if (!props.value) {
    dataUrl.value = ''
    return
  }
  try {
    dataUrl.value = await QRCode.toDataURL(props.value, { margin: 1, width: 240, errorCorrectionLevel: 'M' })
  } catch (caught) {
    dataUrl.value = ''
    error.value = caught instanceof Error ? `二维码生成失败：${caught.message}` : '二维码生成失败'
  }
}

watch(() => props.value, () => void render(), { immediate: true })
</script>

<template>
  <!-- 二维码必须保留白底才能被扫描；其余跟随主题变量 -->
  <Card class="inline-flex w-max max-w-full flex-col items-center gap-2 bg-white p-3 text-neutral-900">
    <figure class="m-0 flex flex-col items-center gap-2" :aria-label="caption ?? '订阅二维码'">
      <img
        v-if="dataUrl"
        :src="dataUrl"
        alt="订阅二维码"
        width="200"
        height="200"
        class="block [image-rendering:pixelated]"
      />
      <p v-else-if="error" class="text-sm" role="alert">{{ error }}</p>
      <p v-else class="text-sm text-muted-foreground">没有可生成二维码的内容</p>
      <figcaption class="max-w-[220px] text-center text-xs text-muted-foreground">
        {{ caption ?? '二维码包含完整订阅链接，等同于密码；请勿截图外传。' }}
      </figcaption>
    </figure>
  </Card>
</template>
