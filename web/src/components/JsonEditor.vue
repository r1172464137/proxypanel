<script setup lang="ts">
import { ref, useId, watch } from 'vue'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { jsonEqual, parseJsonObject, stringifyJson, type JsonObject } from '@/lib/json'

const props = withDefaults(defineProps<{ modelValue: JsonObject; label?: string; hint?: string }>(), {
  label: 'JSON',
  hint: '',
})
const emit = defineEmits<{ 'update:modelValue': [JsonObject]; 'update:error': [string] }>()

const text = ref(stringifyJson(props.modelValue))
const error = ref('')
const fieldId = useId()

function apply(value: string): void {
  text.value = value
  const parsed = parseJsonObject(value, props.label)
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
    const parsed = parseJsonObject(text.value, 'JSON')
    if (parsed.ok && jsonEqual(parsed.value, value)) return
    text.value = stringifyJson(value)
    error.value = ''
    emit('update:error', '')
  },
  { deep: true },
)

function formatText(): void {
  const parsed = parseJsonObject(text.value, props.label)
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
