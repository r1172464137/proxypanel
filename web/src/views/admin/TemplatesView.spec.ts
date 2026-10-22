import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import type { TemplateRecord } from '@/api/types'
import TemplatesView from './TemplatesView.vue'

const fixture = vi.hoisted(() => ({ items: [] as TemplateRecord[] }))

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  remove: vi.fn(),
}))

vi.mock('@/api/endpoints', () => ({
  adminApi: {
    templates: {
      list: mocks.list,
      create: mocks.create,
      update: mocks.update,
    },
  },
}))

// 删除端点由页面直接走 api.delete；这里只替换该方法，保留 ApiError 等真实实现。
vi.mock('@/api/http', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/http')>()
  return { ...actual, api: { ...actual.api, delete: mocks.remove } }
})

const template: TemplateRecord = {
  id: 'tpl_01',
  version: 3,
  name: '默认 xray',
  format: 'xray',
  content: { outbounds: [], inbounds: [] },
  description: '合租默认模板',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-02T00:00:00Z',
}

function mountView() {
  return mount(TemplatesView, { attachTo: document.body })
}

/** 弹窗经由 AppModal 的 Teleport 渲染到 body，断言一律走真实 DOM，避免依赖 stub。 */
function allButtons(): HTMLButtonElement[] {
  return [...document.querySelectorAll('button')] as HTMLButtonElement[]
}

async function click(element: Element): Promise<void> {
  ;(element as HTMLButtonElement).click()
  await flushPromises()
  await nextTick()
}

async function clickButton(label: string): Promise<void> {
  const button = allButtons().find((item) => item.textContent?.trim().includes(label))
  expect(button, `按钮「${label}」应存在`).toBeTruthy()
  await click(button!)
}

async function clickModalFooterButton(label: string): Promise<void> {
  const scope = document.querySelector('.modal__footer') ?? document
  const button = [...scope.querySelectorAll('button')].find((item) => item.textContent?.trim().includes(label))
  expect(button, `弹窗按钮「${label}」应存在`).toBeTruthy()
  await click(button!)
}

function field(selector: string): HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement {
  const element = document.querySelector(selector)
  expect(element, `字段 ${selector} 应存在`).toBeTruthy()
  return element as HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement
}

async function fill(selector: string, value: string): Promise<void> {
  const element = field(selector)
  element.value = value
  element.dispatchEvent(new Event('input', { bubbles: true }))
  element.dispatchEvent(new Event('change', { bubbles: true }))
  await nextTick()
}

beforeEach(() => {
  fixture.items = []
  mocks.list.mockReset().mockImplementation(async () => ({ items: fixture.items, next_cursor: null }))
  mocks.create.mockReset().mockResolvedValue(template)
  mocks.update.mockReset().mockResolvedValue(template)
  mocks.remove.mockReset().mockResolvedValue({ deleted: true, id: template.id })
})

afterEach(() => {
  document.body.innerHTML = ''
})

describe('TemplatesView', () => {
  it('无模板时展示说明与新建入口，而不是空表格', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.text()).toContain('还没有客户端模板')
    expect(wrapper.text()).toContain('xray')
    expect(wrapper.findAll('button').some((button) => button.text().includes('新建模板'))).toBe(true)
    wrapper.unmount()
  })

  it('列表展示模板名称、格式、版本与操作', async () => {
    fixture.items = [template]
    const wrapper = mountView()
    await flushPromises()

    const row = wrapper.find('table tbody tr')
    expect(row.exists()).toBe(true)
    expect(row.text()).toContain('默认 xray')
    expect(row.text()).toContain('tpl_01')
    expect(row.text()).toContain('xray')
    expect(row.text()).toContain('3')
    expect(row.text()).toContain('合租默认模板')

    await click(row.findAll('button').find((button) => button.text() === '编辑')!.element)
    expect(field('#tpl-name').value).toBe('默认 xray')
    expect(field('#tpl-format').value).toBe('xray')
    wrapper.unmount()
  })

  it('新建模板提交 TemplateWrite 并刷新列表', async () => {
    const wrapper = mountView()
    await flushPromises()

    await clickButton('新建模板')
    await fill('#tpl-name', '  新模板  ')
    await fill('#tpl-format', 'mihomo')
    await clickModalFooterButton('保存')
    await flushPromises()

    expect(mocks.create).toHaveBeenCalledTimes(1)
    expect(mocks.create.mock.calls[0]![0]).toMatchObject({
      name: '新模板',
      format: 'mihomo',
      description: null,
      content: {},
    })
    expect(mocks.list).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('名称必填时不发请求', async () => {
    const wrapper = mountView()
    await flushPromises()

    await clickButton('新建模板')
    await clickModalFooterButton('保存')
    await flushPromises()

    expect(mocks.create).not.toHaveBeenCalled()
    // 校验提示在 Teleport 到 body 的弹窗里。
    expect(document.body.textContent).toContain('模板名称必填')
    wrapper.unmount()
  })

  it('编辑模板携带 version 提交', async () => {
    fixture.items = [template]
    const wrapper = mountView()
    await flushPromises()

    await click(wrapper.findAll('button').find((button) => button.text() === '编辑')!.element)
    await fill('#tpl-name', '改名后')
    await clickModalFooterButton('保存')
    await flushPromises()

    expect(mocks.update).toHaveBeenCalledTimes(1)
    expect(mocks.update.mock.calls[0]![0]).toBe('tpl_01')
    expect(mocks.update.mock.calls[0]![1]).toMatchObject({ name: '改名后', version: 3 })
    wrapper.unmount()
  })

  it('删除需要确认；确认后调用 DELETE 并刷新', async () => {
    fixture.items = [template]
    const wrapper = mountView()
    await flushPromises()

    await click(wrapper.findAll('button').find((button) => button.text() === '删除')!.element)
    expect(document.querySelector('.modal')).not.toBeNull()
    expect(mocks.remove).not.toHaveBeenCalled()

    await clickModalFooterButton('删除')
    await flushPromises()

    expect(mocks.remove).toHaveBeenCalledWith('/api/v1/admin/templates/tpl_01')
    expect(mocks.list).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
