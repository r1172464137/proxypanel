/**
 * 主题：浅色 / 深色，配色变量定义在 `src/assets/index.css`。
 *
 * 关键：shadcn-vue 的深色变量挂在 `.dark` class 下，并且
 * `@custom-variant dark (&:is(.dark *))` 也是按 class 判定的。
 * 所以这里必须同时设置 **class** 与 **data-theme**：
 *   - class="dark"  → 触发 shadcn 的深色变量与 dark: 变体；
 *   - data-theme    → 供自定义组件/调试读取，保持向后兼容。
 *
 * 之前只设了 data-theme，导致 class 永远是空、深色变量从未生效
 * （全部页面在"深色"下仍是纯白底）。
 *
 * 没有保存过偏好时默认浅色（不跟随系统深色）；
 * 用户切换后写入 localStorage 的 pp.theme，后续访问沿用该偏好。
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
  const root = document.documentElement
  // class 是 shadcn 深色变量的触发点，必须是 dark/空两种状态
  root.classList.toggle('dark', theme === 'dark')
  root.dataset.theme = theme
  // 让浏览器原生控件（滚动条、输入框）也跟随
  root.style.colorScheme = theme
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
