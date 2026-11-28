import type { NotifyChannelType, NotifyEvent } from '@/api/client'

// 通知渠道类型：名称、一行填写提示、Webhook 地址示例（含 //，绑定到 placeholder，不写成模板里的静态属性，见 AGENTS.md）、
// 加签/签名密钥的名称（没有的不显示密钥框）。
export const channelTypes: Record<NotifyChannelType, { label: string; hint: string; urlExample?: string; secretLabel?: string }> = {
  webhook: { label: '通用 Webhook', hint: '收到 POST JSON，字段见 ⓘ', urlExample: 'https://example.com/hook' },
  dingtalk: {
    label: '钉钉',
    hint: '群设置 → 机器人 → 自定义；关键词填 AGY2API',
    urlExample: 'https://oapi.dingtalk.com/robot/send?access_token=…',
    secretLabel: '加签密钥',
  },
  feishu: {
    label: '飞书',
    hint: '群设置 → 群机器人 → 自定义机器人',
    urlExample: 'https://open.feishu.cn/open-apis/bot/v2/hook/…',
    secretLabel: '签名校验密钥',
  },
  wecom: { label: '企业微信', hint: '群聊 → 添加群机器人，复制 Webhook 地址', urlExample: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…' },
  telegram: { label: 'Telegram', hint: '先给 Bot 发一条消息；Chat ID 可找 @userinfobot 查' },
}

export const channelTypeOrder: NotifyChannelType[] = ['webhook', 'dingtalk', 'feishu', 'wecom', 'telegram']

// 可通知的事件和一行说明（只写频率、附带什么，名称已说明是什么）。
export const notifyEvents: { key: NotifyEvent; label: string; description: string }[] = [
  { key: 'account_banned', label: '账号被条款封禁', description: '附上游给的申诉链接' },
  { key: 'token_invalid', label: 'refresh token 失效', description: '需要重新授权' },
  { key: 'validation_required', label: '账号需要验证', description: '同一账号 6 小时最多一次' },
  { key: 'no_account', label: '没有可用账号', description: '同一模型 1 小时最多一次' },
  { key: 'pool_pause', label: '号池暂停和恢复', description: '开了封禁保护时' },
  { key: 'proxy_failed', label: '代理测试失败', description: '后台测试发现原来正常的代理失败' },
  { key: 'new_version', label: '有新版本', description: '每个版本一次' },
]
