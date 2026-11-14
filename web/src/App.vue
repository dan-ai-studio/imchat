<script setup lang="ts">
import { onMounted, ref } from 'vue'
import Login from './components/Login.vue'
import Chat from './components/Chat.vue'
import { api, type Me } from './api'

const me = ref<Me | null>(null)
const booting = ref(true)

onMounted(async () => {
  try {
    me.value = await api.me()
  } catch {
    me.value = null
  } finally {
    booting.value = false
  }
})
</script>

<template>
  <div v-if="booting" class="center"><span class="muted">loading…</span></div>
  <Login v-else-if="!me" @done="me = $event" />
  <Chat v-else :me="me" @logout="me = null" />
</template>
