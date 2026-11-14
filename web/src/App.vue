<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import Login from './components/Login.vue'
import Chat from './components/Chat.vue'
import Admin from './components/Admin.vue'
import { api, type Me } from './api'

const me = ref<Me | null>(null)
const booting = ref(true)
const route = ref(location.hash)

function syncRoute() {
  route.value = location.hash
}

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

onUnmounted(() => window.removeEventListener('hashchange', syncRoute))
</script>

<template>
  <div v-if="booting" class="center"><span class="muted">loading…</span></div>
  <Login v-else-if="!me" @done="me = $event" />
  <Admin v-else-if="route === '#/admin' && me.is_admin" :me="me" />
  <Chat v-else :me="me" @logout="me = null" />
</template>
