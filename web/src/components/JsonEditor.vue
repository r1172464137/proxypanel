<script setup lang="ts">
import { ref, useId, watch } from 'vue'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { jsonEqual, parseJsonArray, parseJsonObject, stringifyJson, type JsonObject } from '@/lib/json'

const props = withDefaults(defineProps<{ modelValue: JsonObject | unknown[]; label?: string; hint?: string; shape?: 'object' | 'array' }>(), {
  label: 'JSON',
  hint: '',
  shape: 'object',
})
// 值形状由调用方通过 shape 声明（对象或数组），因此这里不做类型收窄。
const emit = defineEmits<{ 'update:modelValue': [unknown]; 'update:error': [string] }>()

const text = ref(stringifyJson(props.modelValue))
const error = ref('')
const fieldId = useId()

// externalProxy 之类是数组；形状由调用方声明，不能只认对象。
// 三处（apply / watch / format）必须用同一个解析器，否则数组模式下 watch 会把
// 用户正在编辑的 [ ... ] 当成解析失败，然后回写成对象。
function parseByShape(value: string) {
  return props.shape === 'array' ? parseJsonArray(value, props.label) : parseJsonObject(value, props.label)
}

function apply(value: string): void {
  text.value = value
  const parsed = parseByShape(value)
  if (!parsed.ok) {
    error.value = parsed.error
    emit('update:error', parsed.error)
    return
  }
  error.value = ''
  emit('update:error', '')
  emit('update:modelValue', parsed.value)
}

watch(
  () => props.modelValue,
  (value) => {
    const parsed = parseByShape(text.value)
    if (parsed.ok && jsonEqual(parsed.value, value)) return
    text.value = stringifyJson(value)
    error.value = ''
    emit('update:error', '')
  },
  { deep: true },
)

function formatText(): void {
  const parsed = parseByShape(text.value)
  if (parsed.ok) {
    text.value = stringifyJson(parsed.value)
    emit('update:modelValue', parsed.value)
  }
}
</script>

<template>
  <div class="flex flex-col gap-2">
    <div class="flex items-center justify-between gap-2">
      <Label :for="fieldId">{{ label }}</Label>
      <Button type="button" variant="outline" size="sm" @click="formatText">格式化</Button>
    </div>
    <Textarea
      :id="fieldId"
      :model-value="text"
      spellcheck="false"
      :aria-invalid="error ? 'true' : 'false'"
      class="min-h-40 font-mono text-xs"
      @update:model-value="apply(String($event))"
    />
    <p v-if="error" class="text-xs text-destructive" role="alert">{{ error }}</p>
    <p v-if="hint" class="text-xs text-muted-foreground">{{ hint }}</p>
    <p v-else class="text-xs text-muted-foreground">
      原生 JSON 编辑器：未在结构化表单中出现的键会原样保留并提交。
    </p>
  </div>
</template>
