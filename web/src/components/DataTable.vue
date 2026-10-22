<script setup lang="ts" generic="T">
/**
 * 统一列表容器。
 *
 * 27 个视图里有 11 个是"分页列表"，此前每个页面各写一遍
 * ErrorState / LoadingBlock / EmptyState / table / 上一页下一页，
 * 重写时若不收敛，会得到 11 种略有差异的表格。
 *
 * 这里只负责**呈现**：三态（错/加载/空）+ 表格 + 分页按钮。
 * 数据加载仍由各页面的 usePagedList 负责，保持后端 cursor 分页语义不变
 * （不引入前端分页，否则与后端 next_cursor 冲突）。
 */
import type { PagedList } from '@/lib/paged'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'

export interface Column {
  /** 列头文字 */
  label: string
  /** 宽度类，如 'w-32'；不填则自适应 */
  width?: string
  /** 在窄屏隐藏该列 */
  hideOnMobile?: boolean
  /** 右对齐（数字列） */
  numeric?: boolean
}

const props = withDefaults(
  defineProps<{
    /** usePagedList 的返回值 */
    list: PagedList<T>
    columns: Column[]
    /** 空状态标题 */
    emptyTitle: string
    /** 空状态说明 */
    emptyDescription?: string
    /** 加载时的说明 */
    loadingLabel?: string
    /** 行数说明，如"本页 3 名" */
    countLabel?: string
    /** 是否隐藏底部分页栏（数据不足一页时） */
    hidePagerWhenSingle?: boolean
  }>(),
  { emptyDescription: '', loadingLabel: '正在读取', countLabel: '', hidePagerWhenSingle: true },
)

// 槽位类型声明：页面通过 row 槽渲染单元格，通过 toolbar / empty-action 插入操作。
defineSlots<{
  row(props: { item: T; index: number; column: Column; columnIndex: number }): unknown
  toolbar(): unknown
  'empty-action'(): unknown
}>()
</script>

<template>
  <div class="space-y-4">
    <div v-if="$slots.toolbar" class="flex flex-wrap items-center gap-2">
      <slot name="toolbar" />
    </div>

    <ErrorState v-if="props.list.error.value" :error="props.list.error.value" @retry="props.list.refresh()" />

    <LoadingBlock
      v-else-if="props.list.loading.value && !props.list.items.value.length"
      :label="props.loadingLabel"
    />

    <EmptyState
      v-else-if="!props.list.items.value.length"
      :title="props.emptyTitle"
      :description="props.emptyDescription"
    >
      <slot name="empty-action" />
    </EmptyState>

    <template v-else>
      <div class="rounded-lg border border-border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead
                v-for="(column, ci) in props.columns"
                :key="ci"
                :class="[
                  column.width,
                  column.numeric ? 'text-right' : '',
                  column.hideOnMobile ? 'hidden md:table-cell' : '',
                ]"
              >
                {{ column.label }}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="(item, index) in props.list.items.value" :key="index">
              <TableCell
                v-for="(column, ci) in props.columns"
                :key="ci"
                :class="[
                  column.numeric ? 'text-right tabular' : '',
                  column.hideOnMobile ? 'hidden md:table-cell' : '',
                ]"
              >
                <slot name="row" :item="item" :index="index" :column="column" :column-index="ci" />
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>

      <div
        v-if="!props.hidePagerWhenSingle || props.list.page.value > 1 || props.list.nextCursor.value"
        class="flex flex-wrap items-center justify-between gap-2"
      >
        <span class="text-xs text-muted-foreground">
          第 {{ props.list.page.value }} 页
          <template v-if="props.countLabel"> · {{ props.countLabel }}</template>
        </span>
        <div class="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            :disabled="props.list.page.value <= 1 || props.list.loading.value"
            @click="props.list.prev()"
          >
            上一页
          </Button>
          <Button
            variant="outline"
            size="sm"
            :disabled="!props.list.nextCursor.value || props.list.loading.value"
            @click="props.list.next()"
          >
            下一页
          </Button>
        </div>
      </div>
    </template>
  </div>
</template>
