import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describeAccessStep, knownAccessSteps } from './accessReasons'

// 这组测试直接读后端的权限判定源码，保证前端的步骤中文名不会与后端漂移。
//
// 起因：/me/status 的判定表曾把 binding_validity 显示成英文——前端那张表里
// 写的是 binding / binding_expired（后端根本不存在这两个名），真正存在的
// binding_validity 反而漏了。手抄一份映射迟早会再错，所以让测试去读源码。

const ACCESS_GO = resolve(__dirname, '../../../internal/panel/access/access.go')

function backendSteps(): string[] {
  const src = readFileSync(ACCESS_GO, 'utf8')
  const names = new Set<string>()
  // 判定链里有两个来源：成功路径的 Step{Step:"…"}，以及失败路径的
  // fail("…", Reason…)。两者都会出现在 /me/status 的判定表里，都要覆盖。
  for (const m of src.matchAll(/Step\{Step:\s*"([a-z_]+)"/g)) names.add(m[1]!)
  for (const m of src.matchAll(/return fail\("([a-z_]+)"/g)) names.add(m[1]!)
  return [...names].sort()
}

describe('权限判定步骤的中文名', () => {
  it('后端源码可读（否则这个测试失去意义）', () => {
    expect(() => readFileSync(ACCESS_GO, 'utf8')).not.toThrow()
  })

  it('后端每一个判定步骤都有中文名', () => {
    const steps = backendSteps()
    expect(steps.length).toBeGreaterThan(0)
    for (const step of steps) {
      const label = describeAccessStep(step)
      // 没有中文名时函数会原样回显，表现为"标签 === 步骤名"
      expect(label, `缺少 ${step} 的中文名`).not.toBe(step)
      expect(label).toMatch(/[\u4e00-\u9fa5]/)
    }
  })

  it('前端没有后端不存在的步骤名（避免再出现 binding_expired 这种错）', () => {
    const backend = new Set(backendSteps())
    for (const known of knownAccessSteps()) {
      expect(backend.has(known), `${known} 在后端不存在`).toBe(true)
    }
  })

  it('未收录的步骤原样回显，方便发现后端新增', () => {
    expect(describeAccessStep('brand_new_step')).toBe('brand_new_step')
    expect(describeAccessStep(undefined)).toBe('—')
    expect(describeAccessStep('')).toBe('—')
  })
})
