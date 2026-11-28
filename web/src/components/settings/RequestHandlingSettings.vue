<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, errorMessage, type RequestHandling, type RequestHandlingResponse } from '@/api/client'
import { Switch } from '@/components/ui/switch'
import SettingGrid from './SettingGrid.vue'
import SettingCard from './SettingCard.vue'
import SettingRow from './SettingRow.vue'
import RejectedContextsCard from './RejectedContextsCard.vue'
import UnsavedBar from './UnsavedBar.vue'
import { toastError } from '@/lib/toast'

const router = useRouter()
const form = ref<RequestHandling | null>(null)
const saved = ref('')
const error = ref('')
const saving = ref(false)

const dirty = computed(() => !!form.value && JSON.stringify(form.value) !== saved.value)

function apply(res: RequestHandlingResponse) {
  form.value = res.settings
  saved.value = JSON.stringify(res.settings)
}

function handle(e: unknown) {
  error.value = errorMessage(e)
}

onMounted(async () => {
  try {
    apply(await api.get<RequestHandlingResponse>('/api/admin/settings/request-handling'))
  } catch (e) {
    handle(e)
  }
})

async function save() {
  if (!form.value) return
  saving.value = true
  try {
    apply(await api.put<RequestHandlingResponse>('/api/admin/settings/request-handling', form.value))
  } catch (e) {
    toastError('保存请求处理设置失败', e)
  } finally {
    saving.value = false
  }
}

function discard() {
  form.value = JSON.parse(saved.value)
}
</script>

<!-- 设置页"请求处理"标签：远程图片内联、思考内容返回。 -->
<template>
  <div v-if="form" class="space-y-6">
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <SettingGrid>
      <SettingCard title="远程图片">
        <template #info>
          <p>客户端把图片以 http(s) 链接放在请求里时，上游不接受任意链接；开启后由网关把图片下载下来内联进请求。</p>
          <p>下载只允许连公网地址（禁内网、环回、云元数据等），单次最多 8 张、合计 10 MiB。</p>
        </template>
        <SettingRow label="内联远程图片" description="服务器会按链接对外下载图片" input-id="rh-images">
          <Switch id="rh-images" v-model="form.remote_images_enabled" />
        </SettingRow>
      </SettingCard>

      <SettingCard title="思考内容">
        <template #info>
          <p>思考模型会先思考再回答。关闭后客户端收不到思考过程（省流量、客户端不显示思考），模型仍按预算思考，回答质量不变。</p>
        </template>
        <SettingRow label="返回思考内容" description="关闭后只返回最终回答" input-id="rh-thoughts">
          <Switch id="rh-thoughts" v-model="form.return_thoughts" />
        </SettingRow>
      </SettingCard>
    </SettingGrid>

    <!-- 被拒上下文拦截有自己的开关（切换即保存）和列表，占满整行 -->
    <RejectedContextsCard />
    <UnsavedBar :dirty="dirty" :saving="saving" @save="save" @discard="discard" />
  </div>
</template>
