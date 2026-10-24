<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { Check, Copy, Eye, EyeOff } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { copyText } from '@/lib/clipboard'
import { pushToast } from '@/lib/toast'

const props = withDefaults(
  defineProps<{
    value: string
    label?: string
    mask?: boolean
    warning?: string
    revealable?: boolean
    successLabel?: string
  }>(),
  { label: '', mask: true, warning: '', revealable: true },
)

const revealed = ref(false)
const copied = ref(false)
const fieldId = useId()
const root = ref<HTMLDivElement | null>(null)
const failed = ref(false)
const copying = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
watch(() => props.value, () => { copied.value = false; failed.value = false; revealed.value = false })
onBeforeUnmount(() => clearTimeout(timer))

const MASK_HEAD = 6
const MASK_TAIL = 4

const display = computed(() => {
  if (!props.mask || revealed.value) return props.value
  if (!props.value) return '—'
  if (props.value.length <= MASK_HEAD + MASK_TAIL) return `${props.value.slice(0, MASK_HEAD)}…`
  return `${props.value.slice(0, MASK_HEAD)}…${props.value.slice(-MASK_TAIL)}`
})

async function doCopy(): Promise<void> {
  if (copying.value || !props.value) return
  const value = props.value
  copying.value = true
  let ok = false
  try { ok = await copyText(value) } catch { /* Manual selection below also covers browser exceptions. */ }
  copying.value = false
  if (value !== props.value) return
  failed.value = !ok
  if (ok) {
    copied.value = true
    clearTimeout(timer)
    timer = setTimeout(() => (copied.value = false), 2000)
    pushToast('success', props.successLabel || '已复制到剪贴板', props.warning || undefined)
  } else {
    copied.value = false
    if (props.revealable) revealed.value = true
    await nextTick()
    if (props.revealable) {
      const input = root.value?.querySelector('input')
      input?.focus()
      input?.select()
    }
    pushToast('error', '复制失败', '浏览器拒绝访问剪贴板，请手动选中文本复制')
  }
}
</script>

<template>
  <div ref="root" class="min-w-0 flex flex-col gap-2">
    <Label v-if="label || $slots.label" :for="fieldId" class="text-sm font-medium">
      <slot name="label">{{ label }}</slot>
    </Label>

    <div class="flex flex-wrap items-start gap-2">
      <Input
        :id="fieldId"
        readonly
        :model-value="display"
        :aria-label="label || undefined"
        class="min-h-11 min-w-0 flex-1 basis-40 font-mono text-xs"
      />
      <div class="flex shrink-0 gap-1">
        <Button
          v-if="revealable && mask"
          type="button"
          variant="outline"
          size="sm"
          class="min-h-11"
          :aria-label="revealed ? '隐藏数值' : '显示数值'"
          @click="revealed = !revealed"
        >
          <EyeOff v-if="revealed" aria-hidden="true" />
          <Eye v-else aria-hidden="true" />
          <span>{{ revealed ? '隐藏' : '显示' }}</span>
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          class="min-h-11"
          :disabled="!value || copying"
          :aria-label="copied ? '已复制' : '复制'"
          @click="doCopy"
        >
          <Check v-if="copied" aria-hidden="true" />
          <Copy v-else aria-hidden="true" />
          <span>{{ copied ? '已复制' : '复制' }}</span>
        </Button>
      </div>
    </div>

    <p v-if="failed" role="status" class="text-sm">复制失败，{{ revealable ? '已显示原文，请手动选择文本复制。' : '请使用允许查看原文的入口手动复制。' }}</p>
    <p v-if="copied" role="status" class="text-sm">{{ successLabel || '已复制到剪贴板' }}</p>
    <p v-if="warning" class="text-xs text-muted-foreground">{{ warning }}</p>
    <p v-else-if="mask" class="text-xs text-muted-foreground">
      默认脱敏显示；此值等同于凭据，请勿转发到不可信渠道。
    </p>
  </div>
</template>
