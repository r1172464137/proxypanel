<script setup lang="ts">
/**
 * 按 streamSchema 的字段声明渲染一个控件。
 *
 * 这样做的原因：Xray 入站有 50 多个可配置字段，逐个手写模板既容易漏、
 * 又容易让读写路径和标签对不上。表在 streamSchema.ts 里声明，这里只负责
 * 把 kind 映射成控件、把错误显示在字段下面。
 */
import { computed } from 'vue'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { StreamField, StreamValue } from '@/lib/streamSchema'

const props = defineProps<{
  field: StreamField
  modelValue: StreamValue
  error?: string
  disabled?: boolean
}>()

const emit = defineEmits<{ 'update:modelValue': [StreamValue] }>()

const id = computed(() => `stream-field-${props.field.key}`)
const invalid = computed(() => Boolean(props.error))
const invalidClass = computed(() => (invalid.value ? 'border-destructive focus-visible:ring-destructive' : ''))

function onText(value: string | number): void {
  emit('update:modelValue', String(value))
}

function onBool(value: boolean | 'indeterminate'): void {
  emit('update:modelValue', value === true)
}

function onSelect(value: string): void {
  emit('update:modelValue', value)
}

const textValue = computed(() => String(props.modelValue ?? ''))
const boolValue = computed(() => props.modelValue === true)
</script>

<template>
  <div class="space-y-1.5" :class="field.kind === 'list' && field.key === 'realityServerNames' ? 'sm:col-span-2' : ''">
    <Label v-if="field.kind !== 'bool'" :for="id">{{ field.label }}</Label>

    <select
      v-if="field.kind === 'select'"
      :id="id"
      :value="textValue"
      :disabled="disabled"
      :aria-invalid="invalid"
      :class="['flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2', invalidClass]"
      @change="onSelect(($event.target as HTMLSelectElement).value)"
    >
      <option v-for="option in field.options ?? []" :key="option" :value="option">
        {{ option === '' ? '不设置' : option }}
      </option>
    </select>

    <label
      v-else-if="field.kind === 'bool'"
      class="flex items-center gap-2 text-sm font-normal leading-snug"
    >
      <input
        :id="id"
        type="checkbox"
        class="size-4 rounded border-input"
        :checked="boolValue"
        :disabled="disabled"
        @change="onBool(($event.target as HTMLInputElement).checked)"
      />
      <span>{{ field.label }}</span>
    </label>

    <Input
      v-else
      :id="id"
      :model-value="textValue"
      :type="field.kind === 'password' ? 'password' : field.kind === 'number' ? 'number' : 'text'"
      :class="['font-mono', invalidClass]"
      :placeholder="field.placeholder"
      :disabled="disabled"
      :aria-invalid="invalid"
      autocomplete="off"
      @update:model-value="onText"
    />

    <p v-if="error" class="text-xs text-destructive">{{ error }}</p>
    <p v-else-if="field.hint" class="text-xs text-muted-foreground">{{ field.hint }}</p>
  </div>
</template>
