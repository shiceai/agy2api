<!-- 设置卡片的排列：内容区够宽时两列瀑布流（先排满左列再排右列，卡片不跨列断开），窄时一列。
     铺满宽度靠控件，不靠说明文字。大块内容（模板文本框、价格表）不放进来，单独占满整行。 -->
<template>
  <div class="gap-6 @5xl/main:columns-2 *:mb-6 *:break-inside-avoid">
    <slot />
  </div>
</template>
