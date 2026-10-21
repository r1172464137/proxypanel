/**
 * 主题：浅色=纸白暖灰，深色=墨黑暖灰。
 * 没有保存过偏好时默认浅色（不跟随系统深色），保证首次进入是纸白界面；
 * 用户在顶部切换后写入 localStorage 的 pp.theme，后续访问沿用该偏好。
 */
import { reactive, watch } from 'vue'

export type ThemeName = 'light' | 'dark'

const STORAGE_KEY = 'pp.theme'

function storedTheme(): ThemeName | null {
  try {
    const value = window.localStorage.getItem(STORAGE_KEY)
    return value === 'light' || value === 'dark' ? value : null
  } catch {
    return null
  }
}

export const themeState = reactive<{ theme: ThemeName }>({ theme: storedTheme() ?? 'light' })

export function applyTheme(theme: ThemeName): void {
  document.documentElement.dataset.theme = theme
}

export function setTheme(theme: ThemeName): void {
  themeState.theme = theme
  try {
    window.localStorage.setItem(STORAGE_KEY, theme)
  } catch {
    // 存储不可用时仅本页生效
  }
}

export function toggleTheme(): void {
  setTheme(themeState.theme === 'dark' ? 'light' : 'dark')
}

export function initTheme(): void {
  applyTheme(themeState.theme)
  watch(
    () => themeState.theme,
    (value) => applyTheme(value),
  )
}
