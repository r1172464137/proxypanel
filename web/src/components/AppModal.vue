<script setup lang="ts">
import { nextTick, onBeforeUnmount, watch } from 'vue'
import { X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<{ open: boolean; title: string; size?: 'sm' | 'md' | 'lg' }>(), {
  size: 'md',
})
const emit = defineEmits<{ close: [] }>()

const sizeClass: Record<'sm' | 'md' | 'lg', string> = {
  sm: 'max-w-md',
  md: 'max-w-2xl',
  lg: 'max-w-4xl',
}

// shadcn 的 Dialog 依赖 reka-ui：Esc、点遮罩、关闭按钮都会把 open 置为 false。
// 这里统一翻译成外部的 close 事件，保持组件对外是受控的。
function onOpenChange(open: boolean): void {
  if (!open) emit('close')
}

// 没有 DialogTrigger 的调用方（本组件就是这种：open 由 props 驱动）reka-ui 无法
// 自己找到“触发元素”，因此手动记录打开前的焦点，关闭后归还。
// reka-ui 的 Dialog 不做背景滚动锁，这里按重写前的行为补上。
let returnFocus: HTMLElement | null = null

function lockScroll(lock: boolean): void {
  document.body.style.overflow = lock ? 'hidden' : ''
}

watch(
  () => props.open,
  (open, wasOpen) => {
    if (open) {
      const active = document.activeElement
      returnFocus = active instanceof HTMLElement ? active : null
      lockScroll(true)
      return
    }
    if (wasOpen) {
      lockScroll(false)
      if (returnFocus && document.contains(returnFocus)) {
        const target = returnFocus
        returnFocus = null
        void nextTick(() => target.focus())
      }
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  returnFocus = null
  lockScroll(false)
})
</script>

<template>
  <Dialog :open="open" @update:open="onOpenChange">
    <DialogContent
      :class="
        cn(
          // shadcn 默认把标题/正文/底部塞进一个 grid 里；本组件自己控制分栏与滚动，
          // 因此清空内边距并把自带的英文关闭按钮藏起来（下面用中文 aria-label 的按钮）。
          // 保留旧的 modal / modal__footer 类名：部分既有视图测试（如 TemplatesView.spec.ts）
          // 通过它们定位弹窗容器；类名本身不再有样式含义，真正的样式全部来自 Tailwind。
          'modal flex max-h-[85vh] flex-col gap-0 overflow-hidden p-0 [&>button]:hidden',
          sizeClass[size],
        )
      "
    >
      <DialogHeader class="flex-row items-center gap-3 space-y-0 border-b border-border px-5 py-3 text-left">
        <DialogTitle class="min-w-0 flex-1 truncate text-base font-semibold">{{ title }}</DialogTitle>
        <DialogDescription class="sr-only">按 Esc 或点击关闭按钮可退出此对话框</DialogDescription>
        <DialogClose as-child>
          <Button variant="ghost" size="icon-sm" aria-label="关闭">
            <X aria-hidden="true" />
          </Button>
        </DialogClose>
      </DialogHeader>

      <div class="min-h-0 flex-1 overflow-y-auto px-5 py-4">
        <slot />
      </div>

      <DialogFooter
        v-if="$slots.footer"
        class="modal__footer flex flex-row flex-wrap justify-end gap-2 border-t border-border bg-muted/40 px-5 py-3"
      >
        <slot name="footer" />
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
