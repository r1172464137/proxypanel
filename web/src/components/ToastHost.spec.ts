import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// vue-sonner 的定位靠它自带的样式表。漏引时 <ol data-sonner-toaster> 会退化成
// 普通文档流元素（右下角变成左下角、压住侧边栏并被视口裁掉），而构建不会报错，
// 所以这里把入口 CSS 的引入列表钉住。
const entry = resolve(process.cwd(), 'src/assets/index.css')

describe('toast 样式入口', () => {
  it('入口 CSS 引入 vue-sonner 的样式表', () => {
    const css = readFileSync(entry, 'utf8')
    expect(css).toMatch(/@import\s+['"]vue-sonner\/style\.css['"]/)
  })
})
