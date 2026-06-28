<script setup lang="ts">
import type { OrganizationInvite, OrganizationSummary, PlatformSettings } from '~/types/accelerator'

const api = useAcceleratorApi()
const auth = useAuth()

const orgs = ref<OrganizationSummary[]>([])
const invites = ref<OrganizationInvite[]>([])
const platform = ref<PlatformSettings | null>(null)
const loading = ref(true)
const error = ref('')
const showCreate = ref(false)
const creating = ref(false)
const createError = ref('')
const form = reactive({ id: '', name: '' })
const accepting = ref<string | null>(null)

const allowCreate = computed(() => platform.value?.allowOrgCreation !== false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [o, inv, plat] = await Promise.all([
      api.listOrganizations(),
      api.listMyInvites(),
      api.getPlatformSettings().catch(() => ({ inviteOnly: false, allowOrgCreation: true, allowAdminBootstrap: true } as PlatformSettings)),
    ])
    orgs.value = o
    invites.value = inv
    platform.value = plat
  }
  catch (e) {
    error.value = api.apiErrorMessage(e)
  }
  finally {
    loading.value = false
  }
}

async function onCreate() {
  createError.value = ''
  if (!allowCreate.value) {
    createError.value = 'This instance is invite-only. Ask an organization admin to invite you.'
    return
  }
  if (!form.name.trim()) {
    createError.value = 'Organization name is required'
    return
  }
  creating.value = true
  try {
    const org = await api.createOrganization(form.name.trim(), form.id.trim() || undefined)
    showCreate.value = false
    form.id = ''
    form.name = ''
    await navigateTo(`/tenants/${encodeURIComponent(org.id)}`)
  }
  catch (e) {
    createError.value = api.apiErrorMessage(e)
  }
  finally {
    creating.value = false
  }
}

async function onAccept(id: string) {
  accepting.value = id
  try {
    const org = await api.acceptInvite(id)
    await navigateTo(`/tenants/${encodeURIComponent(org.id)}`)
  }
  catch (e) {
    error.value = api.apiErrorMessage(e)
  }
  finally {
    accepting.value = null
  }
}

onMounted(() => {
  auth.loadFromStorage()
  if (auth.isLoggedIn.value) load()
})

watch(() => auth.isLoggedIn.value, (v) => { if (v) load() })
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2>Organizations</h2>
        <p class="subtitle">
          Manage backends, proxy tokens, and cache TTL. App queries opt into caching with the
          <code class="mono">_cache</code> collection suffix (default TTL 60s).
        </p>
      </div>
      <button
        v-if="allowCreate"
        class="btn btn-primary"
        type="button"
        @click="showCreate = true"
      >
        + New organization
      </button>
    </div>

    <div
      v-if="platform?.inviteOnly"
      class="alert alert-info mb-3"
    >
      <strong>Invite-only instance.</strong>
      An operator enabled <code class="mono">NANCE_INVITE_ONLY</code> on this server.
      You can sign in, but you can only join organizations you are invited to — creating a new organization is disabled.
      Ask an existing admin or owner to invite your email.
    </div>

    <div v-if="error" class="alert alert-error">{{ error }}</div>

    <section v-if="invites.length" class="card invites-card mb-3">
      <h3>Pending invites</h3>
      <ul class="invite-list">
        <li v-for="inv in invites" :key="inv.id">
          <div>
            <strong>{{ inv.tenantName || inv.tenantId }}</strong>
            <span class="badge">{{ inv.role }}</span>
          </div>
          <button
            class="btn btn-primary btn-sm"
            type="button"
            :disabled="accepting === inv.id"
            @click="onAccept(inv.id)"
          >
            {{ accepting === inv.id ? 'Accepting…' : 'Accept' }}
          </button>
        </li>
      </ul>
    </section>

    <div v-if="loading" class="loading">Loading organizations…</div>

    <div v-else-if="!orgs.length" class="card empty-state">
      <template v-if="platform?.inviteOnly">
        <p><strong>No organizations yet</strong></p>
        <p v-if="invites.length">
          Accept an invite above to get started.
        </p>
        <p v-else>
          This instance is invite-only. When an owner or admin invites
          <strong v-if="auth.user">{{ auth.user.email }}</strong><span v-else>your email</span>,
          the invitation will show up here. Self-serve organization creation is turned off by the operator.
        </p>
      </template>
      <template v-else>
        <p><strong>No organizations yet</strong></p>
        <p>Create an organization to configure a MongoDB backend and issue proxy tokens.</p>
        <button class="btn btn-primary mt-2" type="button" @click="showCreate = true">
          Create organization
        </button>
      </template>
    </div>

    <div v-else class="table-wrap">
      <table class="data-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>ID</th>
            <th>Your role</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="o in orgs" :key="o.id">
            <td>
              <NuxtLink :to="`/tenants/${encodeURIComponent(o.id)}`">{{ o.name }}</NuxtLink>
            </td>
            <td><code>{{ o.id }}</code></td>
            <td><span class="badge">{{ o.role }}</span></td>
            <td>{{ o.status }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="showCreate && allowCreate" class="modal-backdrop" @click.self="showCreate = false">
      <div class="modal card">
        <h3>Create organization</h3>
        <div v-if="createError" class="alert alert-error">{{ createError }}</div>
        <label class="field">
          <span>Name</span>
          <input v-model="form.name" type="text" placeholder="Acme Corp" required>
        </label>
        <label class="field">
          <span>ID <em>(optional slug)</em></span>
          <input v-model="form.id" type="text" placeholder="acme-corp">
        </label>
        <div class="modal-actions">
          <button class="btn btn-ghost" type="button" @click="showCreate = false">Cancel</button>
          <button class="btn btn-primary" type="button" :disabled="creating" @click="onCreate">
            {{ creating ? 'Creating…' : 'Create' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.invites-card h3 { margin-top: 0; }
.invite-list { list-style: none; padding: 0; margin: 0; }
.invite-list li {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0.5rem 0;
  border-bottom: 1px solid var(--border, #2a2f3a);
}
.badge {
  display: inline-block;
  margin-left: 0.5rem;
  padding: 0.1rem 0.45rem;
  border-radius: 999px;
  font-size: 0.7rem;
  text-transform: uppercase;
  background: rgba(91, 141, 239, 0.2);
}
.modal-backdrop {
  position: fixed; inset: 0; background: rgba(0,0,0,0.55);
  display: grid; place-items: center; z-index: 50; padding: 1rem;
}
.modal { width: min(420px, 100%); padding: 1.25rem; display: flex; flex-direction: column; gap: 0.75rem; }
.modal-actions { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.5rem; }
.field { display: flex; flex-direction: column; gap: 0.3rem; font-size: 0.875rem; }
.field input {
  padding: 0.5rem 0.65rem; border-radius: 0.35rem;
  border: 1px solid var(--border, #2a2f3a); background: var(--surface-2, #161a22); color: inherit;
}
.mb-3 { margin-bottom: 1rem; }
.btn-sm { padding: 0.35rem 0.65rem; font-size: 0.8rem; }
</style>
