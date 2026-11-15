<script setup lang="ts">
// ── 阅读导览 · 前端 B（渲染与管理台）────────────────────
// 上一站：前端 A 客户端协议
import { ref } from 'vue'
import { api, type Me } from '../api'

const emit = defineEmits<{ (e: 'done', me: Me): void }>()

const username = ref('alice')
const password = ref('password123')
const error = ref('')
const busy = ref(false)

// 提交：成功后把用户对象交给上层，失败就把后端那句话原样显示出来。
async function submit() {
  error.value = ''
  busy.value = true
  // 成功后把用户交给上层，它决定去聊天还是去管理台。
  try {
    emit('done', await api.login(username.value.trim(), password.value))
  } catch (e) {
    // 后端那句话原样显示：它比任何通用文案都有用。
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}
</script>

<!-- 模板：一个卡片式登录框，错误就在按钮下面，不弹窗。 -->
<template>
  <div class="center">
    <form class="card" @submit.prevent="submit">
      <h1>imchat</h1>
      <!-- 副标题就是产品定位：单文件部署 + 至少一次投递。 -->
      <p class="muted">单机单文件即时通信 · 至少一次投递</p>

      <label>
        <!-- 默认值填 alice：本机演示环境里它是种子账号，省一次输入。 -->
        <span>用户名</span>
        <input v-model="username" autocomplete="username" />
      </label>

      <!-- 密码框用 password 类型，浏览器才会提供自动填充与掩码。 -->
      <label>
        <span>密码</span>
        <input v-model="password" type="password" autocomplete="current-password" />
      </label>

      <!-- 错误显示在按钮上方，内容是后端原话，不换成通用文案。 -->
      <p v-if="error" class="error">{{ error }}</p>

      <button class="primary" type="submit" :disabled="busy">{{ busy ? '登录中…' : '登录' }}</button>

      <!-- 演示账号直接印在页面上：这是本机演示，不是生产登录页。 -->
      <div class="hint">
        <span>演示账号</span>
        <code>alice / bob</code>
        <code>password123</code>
      </div>
    <!-- 表单到此结束；下面全是样式，没有逻辑。 -->
    </form>
  </div>
</template>

<style scoped>
/* 卡片居中：整页只有这一个元素，用 grid 居中即可。 */
.card {
  width: 340px;
  padding: 28px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 16px;
  display: grid;
  gap: 14px;
}
/* 标题与副标题：字号收一点，把注意力留给输入框。 */
h1 { margin: 0; font-size: 22px; letter-spacing: -.02em; }
.muted { color: var(--muted); margin: -6px 0 6px; font-size: 13px; }
label { display: grid; gap: 6px; font-size: 13px; color: var(--muted); }
.error { color: var(--bad); margin: 0; font-size: 13px; }
/* 演示账号那一行：顶部一条细线，把它和表单分开。 */
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
