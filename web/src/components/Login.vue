<script setup lang="ts">
import { ref } from 'vue'
import { api, type Me } from '../api'

const emit = defineEmits<{ (e: 'done', me: Me): void }>()

const username = ref('alice')
const password = ref('password123')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    emit('done', await api.login(username.value.trim(), password.value))
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="center">
    <form class="card" @submit.prevent="submit">
      <h1>imchat</h1>
      <p class="muted">单机单文件即时通信 · 至少一次投递</p>

      <label>
        <span>用户名</span>
        <input v-model="username" autocomplete="username" />
      </label>

      <label>
        <span>密码</span>
        <input v-model="password" type="password" autocomplete="current-password" />
      </label>

      <p v-if="error" class="error">{{ error }}</p>

      <button class="primary" type="submit" :disabled="busy">{{ busy ? '登录中…' : '登录' }}</button>

      <div class="hint">
        <span>演示账号</span>
        <code>alice / bob</code>
        <code>password123</code>
      </div>
    </form>
  </div>
</template>

<style scoped>
.card {
  width: 340px;
  padding: 28px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 16px;
  display: grid;
  gap: 14px;
}
h1 { margin: 0; font-size: 22px; letter-spacing: -.02em; }
.muted { color: var(--muted); margin: -6px 0 6px; font-size: 13px; }
label { display: grid; gap: 6px; font-size: 13px; color: var(--muted); }
.error { color: var(--bad); margin: 0; font-size: 13px; }
.hint {
  display: flex; gap: 8px; align-items: center;
  font-size: 12px; color: var(--muted);
  border-top: 1px solid var(--line); padding-top: 12px;
}
.hint code {
  background: var(--panel-2); border-radius: 6px; padding: 2px 7px;
  font: 12px ui-monospace, Consolas, monospace; color: var(--text);
}
</style>
