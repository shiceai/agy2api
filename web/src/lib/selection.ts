import { ref } from 'vue'

// 顶栏的选择模式：页面有勾选时为 true，顶栏收起页名、页面操作（和手机上的退出按钮），换成页面的选择栏
// （SelectionBar 传送到 App.vue 的 #page-selection）。由 SelectionBar 维护，离开页面时复位。
export const selectionMode = ref(false)
