import type { AgentRecord, EventRecord, TaskRecord } from '@/api/types'

export interface WorkbenchTarget {
  to: string
  label: string
}

// Only a single opaque route segment is accepted. Never interpolate an API URL/path.
function identity(value: unknown): value is string {
  return typeof value === 'string' && /^[a-zA-Z0-9_-]+$/.test(value)
}

function target(type: unknown, id: unknown): WorkbenchTarget | null {
  if (!identity(id)) return null
  switch (type) {
    case 'batch': return { to: `/admin/batches/${id}`, label: `变更批次 ${id}` }
    case 'node': return { to: `/admin/nodes/${id}`, label: `节点 ${id}` }
    case 'server': return { to: `/admin/servers/${id}/runtime`, label: `服务器 ${id} 的运行状态` }
    default: return null
  }
}

export function taskTarget(task: TaskRecord, agents: AgentRecord[] = []): WorkbenchTarget {
  const agentId = task.agent_id ?? task.target_agent_id
  const agent = identity(agentId) ? agents.find(item => (item.id ?? item.agent_id) === agentId) : undefined
  return target('batch', task.batch_id)
    ?? target('node', task.node_id)
    ?? target('server', task.server_id)
    ?? target('server', agent?.server_id)
    ?? { to: '/admin/tasks', label: `任务 ${task.id}：未取得可直达对象身份，请在任务与同步中核对` }
}

export function eventTarget(event: EventRecord): WorkbenchTarget {
  return target(event.subject_type, event.subject_id)
    ?? { to: '/admin/events', label: '对象类型或身份无法直达，查看事件诊断' }
}

export function revisionState(agent: AgentRecord): 'lagging' | 'not-lagging' | 'unknown' {
  const desired = agent.desired_revision
  const applied = agent.applied_revision
  if (typeof desired !== 'number' || typeof applied !== 'number'
    || !Number.isSafeInteger(desired) || !Number.isSafeInteger(applied) || desired < 0 || applied < 0) return 'unknown'
  return desired > applied ? 'lagging' : 'not-lagging'
}

export function agentTarget(agent: AgentRecord): WorkbenchTarget {
  return target('server', agent.server_id)
    ?? { to: '/admin/runtime', label: `管理程序 ${agent.id ?? agent.agent_id ?? '身份未知'}：缺少可直达的服务器身份，查看运行总览` }
}

export function agentNeedsAttention(agent: AgentRecord): boolean {
  return revisionState(agent) === 'lagging' || agent.online === false
}
