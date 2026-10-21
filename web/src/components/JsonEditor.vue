<script setup lang="ts">
import { ref, watch } from 'vue'
import { jsonEqual, parseJsonObject, stringifyJson, type JsonObject } from '@/lib/json'

const props = withDefaults(defineProps<{ modelValue: JsonObject; label?: string; hint?: string }>(), {
  label: 'JSON',
  hint: '',
})
const emit = defineEmits<{ 'update:modelValue': [JsonObject]; 'update:error': [string] }>()

const text = ref(stringifyJson(props.modelValue))
const error = ref('')

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

function onInput(event: Event): void {
  apply((event.target as HTMLTextAreaElement).value)
}
</script>

<template>
  <div class="field">
    <div class="row row--between">
      <label>{{ label }}</label>
      <button type="button" class="btn btn--sm" @click="formatText">格式化</button>
    </div>
    <textarea
      class="textarea textarea--json"
      spellcheck="false"
      :aria-invalid="error ? 'true' : 'false'"
      :value="text"
      @input="onInput"
    />
    <p v-if="error" class="field__help" style="color: var(--danger)" role="alert">{{ error }}</p>
    <p v-if="hint" class="field__help">{{ hint }}</p>
    <p v-else class="field__help">原生 JSON 编辑器：未在结构化表单中出现的键会原样保留并提交。</p>
  </div>
</template>