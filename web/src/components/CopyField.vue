<script setup lang="ts">
import { computed, ref, useId } from 'vue'
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
  }>(),
  { label: '', mask: true, warning: '', revealable: true },
)

const revealed = ref(false)
const copied = ref(false)
const fieldId = useId()

const MASK_HEAD = 6
const MASK_TAIL = 4

const display = computed(() => {
  if (!props.mask || revealed.value) return props.value
  if (!props.value) return '—'
  if (props.value.length <= MASK_HEAD + MASK_TAIL) return `${props.value.slice(0, MASK_HEAD)}…`
  return `${props.value.slice(0, MASK_HEAD)}…${props.value.slice(-MASK_TAIL)}`
})

async function doCopy(): Promise<void> {
  const ok = await copyText(props.value)
  if (ok) {
    copied.value = true
    window.setTimeout(() => (copied.value = false), 2000)
    pushToast('success', '已复制到剪贴板', props.warning || undefined)
  } else {
    pushToast('error', '复制失败', '浏览器拒绝访问剪贴板，请手动选中文本复制')
  }
}
</script>

<template>
  <div class="flex flex-col gap-2">
    <Label v-if="label || $slots.label" :for="fieldId" class="text-sm font-medium">
      <slot name="label">{{ label }}</slot>
    </Label>

    <div class="flex items-start gap-2">
      <Input
        :id="fieldId"
        readonly
        :model-value="display"
        :aria-label="label || undefined"
        class="font-mono text-xs"
      />
      <div class="flex shrink-0 gap-1">
        <Button
          v-if="revealable && mask"
          type="button"
          variant="outline"
          size="sm"
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
          :disabled="!value"
          :aria-label="copied ? '已复制' : '复制'"
          @click="doCopy"
        >
          <Check v-if="copied" aria-hidden="true" />
          <Copy v-else aria-hidden="true" />
          <span>{{ copied ? '已复制' : '复制' }}</span>
        </Button>
      </div>
    </div>

    <p v-if="warning" class="text-xs text-muted-foreground">{{ warning }}</p>
    <p v-else-if="mask" class="text-xs text-muted-foreground">
      默认脱敏显示；此值等同于凭据，请勿转发到不可信渠道。
    </p>
  </div>
</template>
