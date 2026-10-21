import { beforeEach, describe, expect, it, vi } from 'vitest'

// 主题模块在导入时读取 localStorage，因此每个用例都重新加载模块。
async function loadThemeModule(stored?: string) {
  vi.resetModules()
  window.localStorage.clear()
  if (stored) window.localStorage.setItem('pp.theme', stored)
  return await import('./theme')
}

describe('主题偏好', () => {
  beforeEach(() => {
    window.localStorage.clear()
    document.documentElement.removeAttribute('data-theme')
    vi.unstubAllGlobals()
  })

  it('没有保存偏好时默认浅色，即使系统偏好深色', async () => {
    vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: true }))
    const theme = await loadThemeModule()
    expect(theme.themeState.theme).toBe('light')
  })

  it('保留已有的 pp.theme 偏好', async () => {
    const theme = await loadThemeModule('dark')
    expect(theme.themeState.theme).toBe('dark')
  })

  it('忽略非法存储值并回落到浅色', async () => {
    const theme = await loadThemeModule('sepia')
    expect(theme.themeState.theme).toBe('light')
  })

  it('initTheme 应用 data-theme，切换后同步写入存储', async () => {
    const theme = await loadThemeModule()
    theme.initTheme()
    expect(document.documentElement.dataset.theme).toBe('light')

    theme.toggleTheme()
    await Promise.resolve()
    expect(theme.themeState.theme).toBe('dark')
    expect(window.localStorage.getItem('pp.theme')).toBe('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
  })
})
