<!-- 页面主操作（如"导入账号"）：传送到顶栏右侧（App.vue 的 #page-actions），和页名同一行。 -->
<template>
  <Teleport defer to="#page-actions">
    <slot />
  </Teleport>
</template>
