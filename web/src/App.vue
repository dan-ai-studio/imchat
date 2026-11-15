<script setup lang="ts">
// ── 阅读导览 · 前端 B（渲染与管理台）────────────────────
// 上一站：前端 A 客户端协议
// 下一站：没有了，主线到此结束
import { onMounted, onUnmounted, ref } from 'vue'
import Login from './components/Login.vue'
import Chat from './components/Chat.vue'
import Admin from './components/Admin.vue'
import { api, type Me } from './api'

const me = ref<Me | null>(null)
const booting = ref(true)
const route = ref(location.hash)

// 路由只有一个维度：hash。#/admin 是管理台，其余都是聊天。
function syncRoute() {
  route.value = location.hash
}

// 启动时先问一次「我是谁」：失败就是没登录，交给 Login 组件。
onMounted(async () => {
  window.addEventListener('hashchange', syncRoute)
  try {
    me.value = await api.me()
  } catch {
    me.value = null
  } finally {
    booting.value = false
  }
})

// 组件卸载时摘掉监听，否则热更新会叠出多个。
onUnmounted(() => window.removeEventListener('hashchange', syncRoute))
</script>

<template>
  <div v-if="booting" class="center"><span class="muted">loading…</span></div>
  <Login v-else-if="!me" @done="me = $event" />
  <Admin v-else-if="route === '#/admin' && me.is_admin" :me="me" />
  <Chat v-else :me="me" @logout="me = null" />
</template>
