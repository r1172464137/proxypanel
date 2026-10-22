/** 全局轻提示：只在服务端确认成功后调用；失败一律走错误区域。 */
import { reactive } from 'vue'
import { toast as sonner } from 'vue-sonner'

export type ToastTone = 'success' | 'error' | 'info'

export interface ToastItem {
  id: number
  tone: ToastTone
  title: string
  detail?: string
}

let seq = 0
/** 保留导出的本地队列：历史上 UI 依赖它，现由 sonner 负责渲染，这里只维持接口兼容。 */
export const toasts = reactive<ToastItem[]>([])
const sonnerIds = new Map<number, string | number>()

function removeLocal(id: number): void {
  const index = toasts.findIndex((item) => item.id === id)
  if (index >= 0) toasts.splice(index, 1)
}

export function pushToast(tone: ToastTone, title: string, detail?: string): number {
  seq += 1
  const id = seq
  toasts.push({ id, tone, title, detail })

  const show = tone === 'success' ? sonner.success : tone === 'error' ? sonner.error : sonner.info
  const sonnerId = show(title, {
    description: detail,
    duration: tone === 'error' ? 12000 : 6000,
    onDismiss: () => {
      removeLocal(id)
      sonnerIds.delete(id)
    },
    onAutoClose: () => {
      removeLocal(id)
      sonnerIds.delete(id)
    },
  })
  sonnerIds.set(id, sonnerId)

  return id
}

export function dismissToast(id: number): void {
  removeLocal(id)
  const sonnerId = sonnerIds.get(id)
  if (sonnerId !== undefined) {
    sonnerIds.delete(id)
    sonner.dismiss(sonnerId)
  }
}

/** 规则保存成功后的标准提示：明确区分“规则已提交”和“节点已生效”。 */
export function toastRulesSaved(detail = '业务规则已在面板生效；节点分发为异步，请在任务/同步结果中确认实际回执。'): void {
  pushToast('success', '规则已保存', detail)
}
