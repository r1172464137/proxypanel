<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Check, Loader2 } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { redeem } from '@/stores/session'
import { toApiError, describeError } from '@/lib/errors'
import type { ApiError } from '@/api/http'

const route = useRoute()
const router = useRouter()

const code = ref(typeof route.query.code === 'string' ? route.query.code : '')
const username = ref('')
const password = ref('')
const confirm = ref('')
const submitting = ref(false)
const error = ref<ApiError | null>(null)
const localError = ref('')

async function submit(): Promise<void> {
  localError.value = ''
  if (password.value.length < 10) {
    localError.value = '密码至少 10 位'
    return
  }
  if (password.value !== confirm.value) {
    localError.value = '两次输入的密码不一致'
    return
  }
  error.value = null
  submitting.value = true
  try {
    await redeem(code.value.trim(), username.value.trim(), password.value)
    await router.replace('/me/subscription?welcome=1')
  } catch (caught) {
    error.value = toApiError(caught)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="flex min-h-svh items-center justify-center bg-muted/40 px-4 py-10">
    <Card class="w-full max-w-md p-6">
      <div class="min-w-0">
        <h1 class="text-xl font-semibold tracking-tight">兑换邀请</h1>
        <span class="mt-2 block h-0.5 w-6 rounded-none bg-brand" aria-hidden="true" />
        <p class="mt-2 text-sm leading-relaxed text-muted-foreground">
          使用邀请码创建账号。包含服务方案的邀请会一并开通服务；仅建账号的邀请加入后等待管理员开通。
        </p>
      </div>

      <Alert v-if="error" variant="destructive" class="mt-5">
        <AlertTitle>{{ describeError(error).title }}</AlertTitle>
        <AlertDescription>
          <p class="break-all">{{ describeError(error).message }}</p>
          <p class="mt-1 text-xs">如果邀请不可用，请联系邀请你的管理员重新签发。</p>
        </AlertDescription>
      </Alert>

      <Alert
        v-if="localError"
        class="mt-5 border-amber-300/70 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/50 dark:text-amber-200"
      >
        <AlertDescription>{{ localError }}</AlertDescription>
      </Alert>

      <form class="mt-5 flex flex-col gap-4" @submit.prevent="submit">
        <div class="grid gap-2">
          <Label for="redeem-code">邀请码</Label>
          <Input id="redeem-code" v-model="code" class="font-mono" required />
        </div>

        <div class="grid gap-2">
          <Label for="redeem-username">用户名</Label>
          <Input
            id="redeem-username"
            v-model="username"
            autocomplete="username"
            required
            minlength="3"
            maxlength="64"
          />
          <p class="text-xs text-muted-foreground">3–64 位字母、数字、点、下划线或连字符</p>
        </div>

        <div class="grid gap-2">
          <Label for="redeem-password">密码</Label>
          <Input
            id="redeem-password"
            v-model="password"
            type="password"
            autocomplete="new-password"
            required
            minlength="10"
          />
          <p class="text-xs text-muted-foreground">至少 10 位</p>
        </div>

        <div class="grid gap-2">
          <Label for="redeem-confirm">确认密码</Label>
          <Input
            id="redeem-confirm"
            v-model="confirm"
            type="password"
            autocomplete="new-password"
            required
          />
        </div>

        <Button type="submit" class="w-full" :disabled="submitting">
          <Loader2 v-if="submitting" class="animate-spin" aria-hidden="true" />
          <Check v-else aria-hidden="true" />
          {{ submitting ? '兑换中…' : '兑换并登录' }}
        </Button>
      </form>

      <p class="mt-4 text-xs text-muted-foreground">
        已有账号？
        <router-link
          to="/login"
          class="font-medium text-foreground underline underline-offset-4 hover:text-primary"
        >
          返回登录
        </router-link>
      </p>
    </Card>
  </div>
</template>
