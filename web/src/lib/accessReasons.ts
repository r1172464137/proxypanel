/**
 * 访问限制原因的展示文案。
 *
 * 后端在内核里用的是短代码（no_binding / over_quota / ...），这些码会出现在
 * 成员的 access_reasons、proxy_access 里，也会出现在 /sub/{token} 的 403 响应
 * 里。直接把码打给用户看不懂（"no_binding" 是什么？），所以统一在这里翻成
 * 一句人话 + 一句该怎么办。
 *
 * 所有展示访问原因的地方都必须走这个模块，不要在视图里各写一份映射——
 * 之前就有 5 处各显示各的，其中 4 处直接把码打了出来。
 */

export interface AccessReasonText {
  /** 一句话说明现在是什么状态。 */
  label: string
  /** 用户能做什么。 */
  action: string
}

const REASONS: Record<string, AccessReasonText> = {
  no_binding: {
    label: '还没有绑定订阅',
    action: '请联系管理员为你绑定一个订阅，之后才能使用代理。',
  },
  binding_expired: {
    label: '绑定的订阅已到期',
    action: '请联系管理员续期或换一个新的订阅。',
  },
  subscription_access_paused: {
    label: '订阅的访问开关已关闭',
    action: '这个订阅当前不允许使用，请联系管理员开启。',
  },
  subscription_archived: {
    label: '订阅已被归档',
    action: '归档的订阅不再下发，请联系管理员换一个新的。',
  },
  proxy_paused: {
    label: '代理访问已被暂停',
    action: '管理员暂时关闭了你的代理权限，恢复后可继续使用。',
  },
  account_suspended: {
    label: '账户已停用',
    action: '停用期间无法登录，也无法使用代理；请联系管理员恢复。',
  },
  account_archived: {
    label: '账户已归档',
    action: '归档账户不再允许访问，请联系管理员。',
  },
  over_quota: {
    label: '已超出本期流量额度',
    action: '等本期重置，或请管理员调整你的额度。',
  },
  no_nodes_authorized: {
    label: '这个订阅下没有你被授权的节点',
    action: '请联系管理员把你需要的节点加进订阅。',
  },
  node_disabled: {
    label: '授权给你的节点都已停用',
    action: '节点恢复后会重新可用，或联系管理员换一个节点。',
  },
  node_not_authorized: {
    label: '这个节点没有授权给你',
    action: '请联系管理员把这个节点加入你的订阅。',
  },
  expired: {
    label: '已到期',
    action: '请联系管理员续期。',
  },
}

/** 把单个原因码翻成可读文本；未识别的码原样回显以便反馈问题。 */
export function describeAccessReason(code: string): AccessReasonText {
  const known = REASONS[code]
  if (known) return known
  return {
    label: `限制原因：${code}`,
    action: '这是面板暂未收录的原因，请把这条信息发给管理员。',
  }
}

/** 把原因码列表拼成一句短语，用于表格里的紧凑展示。 */
export function accessReasonSummary(codes: string[] | undefined | null): string {
  if (!codes || codes.length === 0) return ''
  return codes.map((code) => describeAccessReason(code).label).join('；')
}

/** 把原因码列表展开成多行，用于详情页与提示条。 */
export function accessReasonDetails(codes: string[] | undefined | null): AccessReasonText[] {
  if (!codes || codes.length === 0) return []
  return codes.map(describeAccessReason)
}
