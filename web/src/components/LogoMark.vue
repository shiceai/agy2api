<!-- 标志：底部三个点（号池里的账号）向上汇成一个箭头，号池托起一个 API；向上也对应 Antigravity（反重力）。
     颜色用主题的 primary，深色模式自动反过来。同一图形的静态版本是 public/favicon.svg 和 README 用的
     docs/assets/logo.svg、logo-dark.svg，改图形时几处一起改（PNG 图标用同样的图形重新渲染）。 -->
<template>
  <svg viewBox="0 0 32 32" aria-hidden="true">
    <rect width="32" height="32" rx="8" class="fill-primary" />
    <path
      d="M9.5 22C9.5 18 16 18 16 14M22.5 22C22.5 18 16 18 16 14M16 22V7.5M12.3 11.2 16 7.5l3.7 3.7"
      fill="none"
      stroke-width="2.4"
      stroke-linecap="round"
      stroke-linejoin="round"
      class="stroke-primary-foreground"
    />
    <g class="fill-primary-foreground">
      <circle cx="9.5" cy="24" r="2" />
      <circle cx="16" cy="24" r="2" />
      <circle cx="22.5" cy="24" r="2" />
    </g>
  </svg>
</template>
