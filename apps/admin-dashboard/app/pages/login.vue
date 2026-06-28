<script setup lang="ts">
definePageMeta({ layout: false })

const api = useAcceleratorApi()
const auth = useAuth()

const step = ref<'email' | 'code'>('email')
const email = ref('')
const code = ref('')
const loading = ref(false)
const error = ref('')
const info = ref('')
const inviteOnly = ref(false)

function needsOnboarding(name?: string | null) {
  return !name || !String(name).trim()
}

onMounted(async () => {
  auth.loadFromStorage()
  try {
    const plat = await api.getPlatformSettings()
    inviteOnly.value = !!plat.inviteOnly
  }
  catch { /* ignore */ }
  if (auth.isLoggedIn.value) {
    navigateTo(needsOnboarding(auth.user.value?.name) ? '/onboarding' : '/')
  }
})

async function sendCode() {
  error.value = ''
  info.value = ''
  if (!email.value.trim()) {
    error.value = 'Email is required'
    return
  }
  loading.value = true
  try {
    await api.requestCode(email.value.trim())
    step.value = 'code'
    info.value = 'Check your email for a 6-digit code (also printed in control plane logs in dev).'
  }
  catch (e) {
    error.value = api.apiErrorMessage(e)
  }
  finally {
    loading.value = false
  }
}

async function verify() {
  error.value = ''
  if (!code.value.trim()) {
    error.value = 'Enter the verification code'
    return
  }
  loading.value = true
  try {
    const res = await api.verifyCode(email.value.trim(), code.value.trim())
    auth.setSession(res.token, res.user)
    await navigateTo(needsOnboarding(res.user?.name) ? '/onboarding' : '/')
  }
  catch (e) {
    error.value = api.apiErrorMessage(e)
  }
  finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card card">
      <div class="login-brand">
        <span class="logo-mark">N</span>
        <div>
          <h1>Nance</h1>
          <p class="subtitle">Sign in with your email</p>
          <p v-if="inviteOnly" class="invite-hint">This server is invite-only — you can sign in, then accept an organization invite. Creating new organizations is disabled.</p>
        </div>
      </div>

      <div v-if="error" class="alert alert-error">{{ error }}</div>
      <div v-if="info" class="alert alert-info">{{ info }}</div>

      <form v-if="step === 'email'" class="stack" @submit.prevent="sendCode">
        <label class="field">
          <span>Email</span>
          <input v-model="email" type="email" autocomplete="email" placeholder="you@company.com" required>
        </label>
        <button class="btn btn-primary" type="submit" :disabled="loading">
          {{ loading ? 'Sending…' : 'Continue' }}
        </button>
      </form>

      <form v-else class="stack" @submit.prevent="verify">
        <p class="muted">Code sent to <strong>{{ email }}</strong></p>
        <label class="field">
          <span>Verification code</span>
          <input v-model="code" type="text" inputmode="numeric" autocomplete="one-time-code" placeholder="123456" required>
        </label>
        <button class="btn btn-primary" type="submit" :disabled="loading">
          {{ loading ? 'Verifying…' : 'Sign in' }}
        </button>
        <button class="btn btn-ghost" type="button" :disabled="loading" @click="step = 'email'">
          Use a different email
        </button>
      </form>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 2rem;
  background: var(--bg, #0f1115);
}
.login-card {
  width: min(420px, 100%);
  padding: 2rem;
}
.login-brand {
  display: flex;
  gap: 1rem;
  align-items: center;
  margin-bottom: 1.5rem;
}
.logo-mark {
  width: 2.5rem;
  height: 2.5rem;
  border-radius: 0.5rem;
  background: var(--accent, #5b8def);
  color: #fff;
  display: grid;
  place-items: center;
  font-weight: 700;
}
.stack { display: flex; flex-direction: column; gap: 1rem; }
.field { display: flex; flex-direction: column; gap: 0.35rem; font-size: 0.875rem; }
.field input {
  padding: 0.6rem 0.75rem;
  border-radius: 0.4rem;
  border: 1px solid var(--border, #2a2f3a);
  background: var(--surface-2, #161a22);
  color: inherit;
}
.muted { font-size: 0.9rem; opacity: 0.85; }
.invite-hint { font-size: 0.8rem; opacity: 0.8; margin: 0.35rem 0 0; max-width: 22rem; line-height: 1.35; }
.alert-info {
  background: rgba(91, 141, 239, 0.12);
  border: 1px solid rgba(91, 141, 239, 0.35);
  padding: 0.75rem;
  border-radius: 0.4rem;
  font-size: 0.875rem;
}
</style>
