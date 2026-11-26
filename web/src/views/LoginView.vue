<script setup lang="ts">
import { onMounted, ref, useTemplateRef } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Loader2 } from '@lucide/vue'
import { ApiError, fetchSession, isDemo, setAdminPassword } from '@/api/client'
import { versionInfo } from '@/lib/version'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import AppLogo from '@/components/AppLogo.vue'

// 登录：提交时先用输入的口令请求 /api/admin/session 当场校验，对了才保存并进入；口令失效被踢回来时
// 地址里带着 redirect，登录后回到原来的页面。
const route = useRoute()
const router = useRouter()
// 演示站的口令固定是 demo，预先填好。
const pw = ref(isDemo ? 'demo' : '')
const error = ref('')
const busy = ref(false)
const input = useTemplateRef<{ $el: HTMLInputElement }>('input')

onMounted(() => input.value?.$el?.focus())

// 只回到站内路径，防止 redirect 被构造成跳到别的网站。
function target(): string {
  const r = route.query.redirect
  return typeof r === 'string' && r.startsWith('/') && !r.startsWith('//') && !r.startsWith('/login') ? r : '/'
}

async function submit() {
  if (!pw.value || busy.value) return
  busy.value = true
  error.value = ''
  try {
    versionInfo.value = await fetchSession(pw.value) // 进去后侧边栏直接有版本号，不跳动
    setAdminPassword(pw.value)
    await router.replace(target())
  } catch (e) {
    // 401 口令不对；429 是错误口令太多被暂时锁定，API 客户端已换成带等待时间的中文说明。
    if (e instanceof ApiError && (e.status === 401 || e.status === 429)) {
      error.value = e.status === 401 ? '口令不对' : e.message
    } else {
      error.value = `连不上服务：${(e as Error).message}`
    }
    input.value?.$el?.focus()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex min-h-dvh flex-col items-center justify-center gap-6 bg-muted/40 p-4">
    <AppLogo />
    <Card class="w-full max-w-sm">
      <CardHeader>
        <CardTitle>登录控制台</CardTitle>
        <CardDescription>{{ isDemo ? '演示站，口令是 demo，数据是模拟的。' : '输入启动时设置的管理口令。' }}</CardDescription>
      </CardHeader>
      <CardContent>
        <form class="grid gap-4" @submit.prevent="submit">
          <Field :data-invalid="!!error || undefined">
            <FieldLabel for="admin-password">管理口令</FieldLabel>
            <Input
              id="admin-password"
              ref="input"
              v-model="pw"
              type="password"
              autocomplete="current-password"
              placeholder="AGY2API_ADMIN_PASSWORD"
              :aria-invalid="!!error || undefined"
              aria-describedby="admin-password-error"
              @input="error = ''"
            />
            <FieldError id="admin-password-error" :errors="error ? [error] : []" />
          </Field>
          <Button type="submit" class="w-full" :disabled="!pw || busy">
            <Loader2 v-if="busy" class="animate-spin" />{{ busy ? '验证中…' : '进入' }}
          </Button>
        </form>
      </CardContent>
    </Card>
  </div>
</template>
