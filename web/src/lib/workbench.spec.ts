import { describe, expect, it } from 'vitest'
import { agentNeedsAttention, agentTarget, eventTarget, revisionState, taskTarget } from './workbench'

describe('workbench source-derived diagnostics', () => {
  it('prioritizes batch, node, then real server identity without inventing task detail or queries', () => {
    const task = { id: 'task-1', batch_id: 'batch-1', node_id: 'node-1', server_id: 'server-1' }
    expect(taskTarget(task).to).toBe('/admin/batches/batch-1')
    expect(taskTarget({ ...task, batch_id: null }).to).toBe('/admin/nodes/node-1')
    expect(taskTarget({ ...task, batch_id: null, node_id: null }).to).toBe('/admin/servers/server-1/runtime')
    expect(taskTarget({ id: 'task-1', agent_id: 'agent-1' }, [{ id: 'agent-1', server_id: 's-1' }]).to).toBe('/admin/servers/s-1/runtime')
    expect(taskTarget({ id: 'task-1', agent_id: 'agent-1' }).to).toBe('/admin/tasks')
    expect(taskTarget({ id: 'task-1' }).label).toContain('未取得可直达对象身份')
  })

  it.each([undefined, null, Number.NaN, Infinity, -1])('keeps incomplete/invalid revisions unknown (%s)', missing => {
    expect(revisionState({ desired_revision: 10, applied_revision: missing })).toBe('unknown')
    expect(revisionState({ desired_revision: missing, applied_revision: 0 })).toBe('unknown')
    expect(agentNeedsAttention({ desired_revision: 10, applied_revision: missing })).toBe(false)
  })

  it('uses both actual revisions, including known zero, without substituting revision aliases', () => {
    expect(revisionState({ desired_revision: 2, applied_revision: 0 })).toBe('lagging')
    expect(revisionState({ desired_revision: 0, applied_revision: 0 })).toBe('not-lagging')
    expect(revisionState({ desired_revision: 2, revision: 1 })).toBe('unknown')
    expect(revisionState({ desired_revision: 1, applied_revision: 2 })).toBe('not-lagging')
    expect(agentNeedsAttention({ online: false })).toBe(true)
    expect(agentNeedsAttention({ status: 'unknown' })).toBe(false)
  })

  it('removes resolved source-derived lag and offline attention on the next source read', () => {
    expect([{ desired_revision: 3, applied_revision: 2, online: false }].filter(agentNeedsAttention)).toHaveLength(1)
    expect([{ desired_revision: 3, applied_revision: 3, online: true }].filter(agentNeedsAttention)).toHaveLength(0)
  })

  it('only resolves allowed event subject types and safe single-segment identities', () => {
    expect(eventTarget({ id: 'e', subject_type: 'node', subject_id: 'n-1' }).to).toBe('/admin/nodes/n-1')
    expect(eventTarget({ id: 'e', subject_type: 'batch', subject_id: 'b-1' }).to).toBe('/admin/batches/b-1')
    expect(eventTarget({ id: 'e', subject_type: 'server', subject_id: 's-1' }).to).toBe('/admin/servers/s-1/runtime')
    for (const subject_id of ['../settings', '//evil.test', 'javascript:alert(1)', 'n?x=1', 'n#x', '%2fsettings', '', 'a/b']) {
      expect(eventTarget({ id: 'e', subject_type: 'node', subject_id }).to).toBe('/admin/events')
      expect(taskTarget({ id: 't', node_id: subject_id }).to).toBe('/admin/tasks')
      expect(agentTarget({ server_id: subject_id }).to).toBe('/admin/runtime')
    }
    expect(eventTarget({ id: 'e', subject_type: 'task', subject_id: 't-1' }).to).toBe('/admin/events')
    expect(eventTarget({ id: 'e', subject_type: 'https://evil.test', subject_id: 'x' }).label).toContain('事件诊断')
  })
})
