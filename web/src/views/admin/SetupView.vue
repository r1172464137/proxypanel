<script setup lang="ts">
import { onMounted } from 'vue'
import { adminApi } from '@/api/endpoints'
import { useResource } from '@/lib/async'
import SetupChecklist from '@/components/SetupChecklist.vue'
import PageHeader from '@/components/PageHeader.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import { Button } from '@/components/ui/button'
const overview = useResource(() => adminApi.overview())
onMounted(() => void overview.reload())
</script>
<template>
  <div class="space-y-6">
    <PageHeader title="首次设置" description="复用已有资源，按真实配置与流量推进；保存配置不等于连接成功。">
      <Button variant="outline" :disabled="overview.loading.value" data-testid="setup-refresh-button" @click="overview.reload()">重新核对进度</Button>
      <Button as-child variant="outline"><router-link to="/admin/overview">返回工作台</router-link></Button>
    </PageHeader>
    <LoadingBlock v-if="overview.loading.value && !overview.data.value" label="正在读取设置进度" />
    <ErrorState v-else-if="overview.error.value" :error="overview.error.value" @retry="overview.reload()" />
    <SetupChecklist v-else :setup="overview.data.value?.setup" />
  </div>
</template>
