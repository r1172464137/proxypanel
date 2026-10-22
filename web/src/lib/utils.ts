import type { ClassValue } from 'clsx'
import { clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

/** shadcn 约定：合并类名，Tailwind 冲突时后者胜出。 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
