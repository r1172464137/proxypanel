<script setup lang="ts">
import { Loader2 } from 'lucide-vue-next'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'

withDefaults(
  defineProps<{
    open: boolean
    title: string
    confirmLabel?: string
    danger?: boolean
    pending?: boolean
    errorText?: string
  }>(),
  { confirmLabel: '确认', danger: false, pending: false, errorText: '' },
)
const emit = defineEmits<{ confirm: []; cancel: [] }>()

// 受控弹窗：Esc / 取消都把 open 置为 false，统一翻译成 cancel。
// 确认按钮不自动关闭（由调用方在业务成功后关闭），保持与重写前一致的事件时序。
function onOpenChange(open: boolean): void {
  if (!open) emit('cancel')
}
</script>

<template>
  <AlertDialog :open="open" @update:open="onOpenChange">
    <!-- 保留旧的 modal / modal__footer 类名：既有视图测试（如 TemplatesView.spec.ts）用它定位弹窗；
         样式全部来自 Tailwind，这两个类名只作为稳定的结构钩子。 -->
    <AlertDialogContent class="modal">
      <AlertDialogHeader>
        <AlertDialogTitle>{{ title }}</AlertDialogTitle>
      </AlertDialogHeader>

      <AlertDialogDescription as="div" class="space-y-3">
        <slot />
        <Alert v-if="errorText" variant="destructive">
          <AlertDescription class="break-all">{{ errorText }}</AlertDescription>
        </Alert>
        <p class="text-xs text-muted-foreground">危险操作会写入审计记录；确认后立即生效。</p>
      </AlertDialogDescription>

      <AlertDialogFooter class="modal__footer gap-2">
        <Button variant="outline" :disabled="pending" @click="emit('cancel')">取消</Button>
        <Button
          :variant="danger ? 'destructive' : 'default'"
          :disabled="pending"
          @click="emit('confirm')"
        >
          <Loader2 v-if="pending" class="animate-spin" aria-hidden="true" />
          {{ pending ? '处理中…' : confirmLabel }}
        </Button>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
