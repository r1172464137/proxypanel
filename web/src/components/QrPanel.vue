<script setup lang="ts">
import { ref, watch } from 'vue'
import QRCode from 'qrcode'

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
  <figure class="qr">
    <img v-if="dataUrl" :src="dataUrl" alt="订阅二维码" width="200" height="200" />
    <p v-else-if="error" class="text-sm" role="alert">{{ error }}</p>
    <p v-else class="text-sm text-muted">没有可生成二维码的内容</p>
    <figcaption class="text-xs text-muted">
      {{ caption ?? '二维码包含完整订阅链接，等同于密码；请勿截图外传。' }}
    </figcaption>
  </figure>
</template>

<style scoped>
.qr {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  align-items: center;
  /* 二维码必须保留白底才能被扫描；其余跟随暖纸体系 */
  background: #fff;
  color: #271610;
  border: 1px solid var(--input);
  border-radius: 0;
  box-shadow: var(--shadow-1);
  padding: var(--space-3);
  width: max-content;
  max-width: 100%;
}

.qr img {
  display: block;
  image-rendering: pixelated;
}

.qr figcaption {
  max-width: 220px;
  text-align: center;
}
</style>