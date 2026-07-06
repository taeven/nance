<script setup lang="ts">
import {
  AlertTriangleIcon,
  CableIcon,
  CopyIcon,
  DatabaseIcon,
  KeyRoundIcon,
  PlusIcon,
  SettingsIcon,
  ShieldIcon,
  Trash2Icon,
  UsersIcon,
} from '@lucide/vue'
import { toast } from 'vue-sonner'
import type {
  CachePolicy,
  CollectionPolicy,
  Connection,
  IssueTokenResponse,
  OrganizationInvite,
  OrganizationMember,
  Tenant,
  Token,
} from '~/types/accelerator'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

const route = useRoute()
const router = useRouter()
const api = useAcceleratorApi()
const tenantId = computed(() => String(route.params.id || ''))

/** Top-level: product work vs organization administration */
const area = ref<'connections' | 'caching' | 'members' | 'org'>('connections')
/** Sub-panel when a connection is selected */
const connPanel = ref<'source' | 'access' | 'cache-write'>('access')
const showAddConnection = ref(false)

const myRole = computed(() => tenant.value?.role || 'member')
const canManage = computed(() => tenant.value?.canManage === true || myRole.value === 'owner' || myRole.value === 'admin')
const canDelete = computed(() => tenant.value?.canDelete === true || myRole.value === 'owner')
const isReadOnly = computed(() => !canManage.value)

const tenant = ref<Tenant | null>(null)
const policy = ref<CachePolicy | null>(null)
const tokens = ref<Token[]>([])
const loading = ref(true)
const error = ref('')
const connectionsLoading = ref(false)

const connections = ref<Connection[]>([])
const selectedConnectionId = ref<string | null>(null)
const connectionBusy = ref(false)
const newConnName = ref('')
const newConnUri = ref('')
const updateUri = ref('')
const deleteConnTarget = ref<string | null>(null)
const deleteConnOpen = ref(false)

const defaultTtl = ref(60)
const defaultsBusy = ref(false)

const newCollKey = ref('')
const newCollTtl = ref(60)
const newCollMaxBytes = ref<number | undefined>(undefined)
const collBusy = ref(false)

const tokenDesc = ref('')
const tokenBusy = ref(false)
const issuedToken = ref<IssueTokenResponse | null>(null)

const invDb = ref('')
const invColl = ref('')
const invTags = ref('')
const invBusy = ref(false)

const members = ref<OrganizationMember[]>([])
const pendingInvites = ref<OrganizationInvite[]>([])
const inviteEmail = ref('')
const inviteRole = ref('member')
const membersBusy = ref(false)

const deleteStep = ref<'warn' | 'code'>('warn')
const deleteCode = ref('')
const deleteBusy = ref(false)
const deleteAck = ref(false)

const revokeTarget = ref<string | null>(null)
const revokeOpen = ref(false)

const inviteRoleOptions = computed(() => {
  if (myRole.value === 'owner') {
    return [
      { value: 'member', label: 'member (read-only)' },
      { value: 'admin', label: 'admin (manage settings)' },
      { value: 'owner', label: 'owner (full control)' },
    ]
  }
  return [
    { value: 'member', label: 'member (read-only)' },
    { value: 'admin', label: 'admin (manage settings)' },
  ]
})

const selectedConnection = computed(() =>
  connections.value.find(c => c.id === selectedConnectionId.value) || null,
)

async function loadTenant() {
  loading.value = true
  error.value = ''
  try {
    tenant.value = await api.getTenant(tenantId.value)
  }
  catch (e) {
    error.value = api.apiErrorMessage(e)
    tenant.value = null
  }
  finally {
    loading.value = false
  }
}

async function loadPolicy() {
  try {
    policy.value = await api.getPolicy(tenantId.value)
    defaultTtl.value = policy.value.defaultTtlSeconds ?? 60
  }
  catch (e) {
    toast.error(`Policy: ${api.apiErrorMessage(e)}`)
  }
}

async function loadConnections() {
  connectionsLoading.value = true
  try {
    connections.value = await api.listConnections(tenantId.value)
    const fromQuery = typeof route.query.conn === 'string' ? route.query.conn : null
    if (fromQuery && connections.value.some(c => c.id === fromQuery)) {
      selectedConnectionId.value = fromQuery
    }
    else if (selectedConnectionId.value && !connections.value.some(c => c.id === selectedConnectionId.value)) {
      selectedConnectionId.value = null
      tokens.value = []
    }
    if (!selectedConnectionId.value && connections.value.length) {
      selectedConnectionId.value = connections.value[0].id
    }
    if (selectedConnectionId.value) {
      await loadTokens()
      syncConnQuery()
    }
  }
  catch (e) {
    toast.error(`Connections: ${api.apiErrorMessage(e)}`)
  }
  finally {
    connectionsLoading.value = false
  }
}

async function loadTokens() {
  if (!selectedConnectionId.value) {
    tokens.value = []
    return
  }
  try {
    tokens.value = await api.listTokens(tenantId.value, selectedConnectionId.value)
  }
  catch (e) {
    toast.error(`Proxy access: ${api.apiErrorMessage(e)}`)
  }
}

function syncConnQuery() {
  const id = selectedConnectionId.value
  const next = { ...route.query } as Record<string, string | string[] | undefined>
  if (id) next.conn = id
  else delete next.conn
  router.replace({ query: next })
}

async function selectConnection(id: string) {
  if (selectedConnectionId.value === id && !showAddConnection.value) return
  selectedConnectionId.value = id
  issuedToken.value = null
  updateUri.value = ''
  tokenDesc.value = ''
  showAddConnection.value = false
  syncConnQuery()
  await loadTokens()
}

function onToggleAddConnection() {
  showAddConnection.value = !showAddConnection.value
  if (showAddConnection.value) {
    selectedConnectionId.value = null
  }
}

async function loadMembers() {
  try {
    const [m, inv] = await Promise.all([
      api.listMembers(tenantId.value),
      api.listTenantInvites(tenantId.value).catch(() => [] as OrganizationInvite[]),
    ])
    members.value = m
    pendingInvites.value = inv
  }
  catch (e) {
    toast.error(`Members: ${api.apiErrorMessage(e)}`)
  }
}

async function sendInvite() {
  if (!inviteEmail.value.trim()) return
  membersBusy.value = true
  try {
    await api.inviteMember(tenantId.value, inviteEmail.value.trim(), inviteRole.value as 'member' | 'admin' | 'owner')
    inviteEmail.value = ''
    toast.success('Invite sent')
    await loadMembers()
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    membersBusy.value = false
  }
}

async function onRemoveMember(userId: string) {
  membersBusy.value = true
  try {
    await api.removeMember(tenantId.value, userId)
    toast.success('Member removed')
    await loadMembers()
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    membersBusy.value = false
  }
}

async function onRevokeInvite(inviteId: string) {
  membersBusy.value = true
  try {
    await api.revokeInvite(tenantId.value, inviteId)
    toast.success('Invite revoked')
    await loadMembers()
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    membersBusy.value = false
  }
}

watch(area, async (t) => {
  if (t === 'caching' && !policy.value) await loadPolicy()
  if (t === 'connections') await loadConnections()
  if (t === 'members') await loadMembers()
})

onMounted(async () => {
  await loadTenant()
  if (tenant.value) {
    await Promise.all([loadPolicy(), loadConnections()])
  }
})

async function createConnection() {
  const name = newConnName.value.trim()
  const uri = newConnUri.value.trim()
  if (!name) {
    toast.error('Name is required')
    return
  }
  if (!uri) {
    toast.error('MongoDB URI is required')
    return
  }
  connectionBusy.value = true
  try {
    const c = await api.createConnection(tenantId.value, name, uri)
    newConnName.value = ''
    newConnUri.value = ''
    showAddConnection.value = false
    toast.success(`Connection “${c.name}” created`)
    selectedConnectionId.value = c.id
    connPanel.value = 'access'
    await loadConnections()
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    connectionBusy.value = false
  }
}

async function saveConnectionUri() {
  if (!selectedConnectionId.value) return
  if (!updateUri.value.trim()) {
    toast.error('MongoDB URI is required')
    return
  }
  connectionBusy.value = true
  try {
    await api.updateConnection(tenantId.value, selectedConnectionId.value, { uri: updateUri.value.trim() })
    updateUri.value = ''
    toast.success('Source URI updated (encrypted at rest)')
    await loadConnections()
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    connectionBusy.value = false
  }
}

async function testSelectedConnection() {
  if (!selectedConnectionId.value) return
  connectionBusy.value = true
  try {
    const res = await api.testConnection(tenantId.value, selectedConnectionId.value)
    toast.success(res.status || 'Connection successful')
    await loadConnections()
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    connectionBusy.value = false
  }
}

async function setAutoInvalidateOnWrite(enabled: boolean) {
  if (!selectedConnectionId.value || !canManage.value) return
  connectionBusy.value = true
  try {
    await api.updateConnection(tenantId.value, selectedConnectionId.value, {
      autoInvalidateOnWrite: enabled,
    })
    toast.success(enabled
      ? 'Auto-invalidate on write enabled'
      : 'Auto-invalidate on write disabled')
    await loadConnections()
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    connectionBusy.value = false
  }
}

function confirmDeleteConnection(id: string) {
  deleteConnTarget.value = id
  deleteConnOpen.value = true
}

async function deleteConnection() {
  if (!deleteConnTarget.value) return
  connectionBusy.value = true
  try {
    await api.deleteConnection(tenantId.value, deleteConnTarget.value)
    toast.success('Connection deleted')
    if (selectedConnectionId.value === deleteConnTarget.value) {
      selectedConnectionId.value = null
      issuedToken.value = null
      tokens.value = []
    }
    await loadConnections()
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    connectionBusy.value = false
    deleteConnOpen.value = false
    deleteConnTarget.value = null
  }
}

async function saveDefaults() {
  defaultsBusy.value = true
  try {
    const ttl = Number(defaultTtl.value)
    if (!ttl || ttl < 1) {
      toast.error('Default TTL must be at least 1 second')
      return
    }
    await api.setDefaultTtl(tenantId.value, ttl)
    await loadPolicy()
    toast.success(`Default cache TTL set to ${ttl}s`)
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    defaultsBusy.value = false
  }
}

async function upsertCollection(key: string, pol: CollectionPolicy) {
  collBusy.value = true
  try {
    await api.setCollectionPolicy(tenantId.value, key, pol)
    await loadPolicy()
    toast.success(`Override saved for ${key}`)
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    collBusy.value = false
  }
}

async function addCollection() {
  const key = newCollKey.value.trim()
  if (!key || !key.includes('.')) {
    toast.error('Use real db.collection format (e.g. mydb.orders)')
    return
  }
  if (key.endsWith('_cache')) {
    toast.error('Use the real collection name (without _cache)')
    return
  }
  const ttl = Number(newCollTtl.value) || Number(defaultTtl.value) || 60
  const pol: CollectionPolicy = { enabled: true, ttlSeconds: ttl }
  if (newCollMaxBytes.value && newCollMaxBytes.value > 0) {
    pol.maxResultBytes = Number(newCollMaxBytes.value)
  }
  await upsertCollection(key, pol)
  newCollKey.value = ''
  newCollMaxBytes.value = undefined
}

async function removeCollectionOverride(key: string) {
  collBusy.value = true
  try {
    await api.setCollectionPolicy(tenantId.value, key, { enabled: true, ttlSeconds: 0 })
    await loadPolicy()
    toast.message(`${key} will inherit the default TTL (${defaultTtl.value}s)`)
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    collBusy.value = false
  }
}

const collectionEntries = computed(() => {
  if (!policy.value?.collections) return []
  return Object.entries(policy.value.collections).map(([key, pol]) => ({ key, ...pol }))
})

function effectiveTtl(row: { ttlSeconds?: number }) {
  if (row.ttlSeconds && row.ttlSeconds > 0) return row.ttlSeconds
  return policy.value?.defaultTtlSeconds ?? defaultTtl.value ?? 60
}

async function issueToken() {
  if (!selectedConnectionId.value) return
  tokenBusy.value = true
  issuedToken.value = null
  try {
    issuedToken.value = await api.issueToken(
      tenantId.value,
      selectedConnectionId.value,
      tokenDesc.value.trim() || undefined,
    )
    tokenDesc.value = ''
    await loadTokens()
    toast.warning('Copy the proxy connection URI now — it is only shown once.')
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    tokenBusy.value = false
  }
}

function issuedProxyUri(): string {
  const issued = issuedToken.value
  if (!issued) return ''
  if (issued.proxyConnectionUri) return issued.proxyConnectionUri
  return `mongodb://${encodeURIComponent(tenantId.value)}:${encodeURIComponent(issued.rawToken)}@127.0.0.1:27018/?authMechanism=PLAIN&authSource=$external`
}

function confirmRevoke(tokenId: string) {
  revokeTarget.value = tokenId
  revokeOpen.value = true
}

async function revokeToken() {
  if (!revokeTarget.value) return
  try {
    await api.revokeToken(revokeTarget.value)
    await loadTokens()
    toast.success('Proxy access revoked')
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    revokeOpen.value = false
    revokeTarget.value = null
  }
}

function copyText(text: string) {
  navigator.clipboard?.writeText(text).then(() => toast.message('Copied to clipboard'))
}

async function runInvalidate() {
  if (!canManage.value) {
    toast.error('Only admins and owners can invalidate cache')
    return
  }
  invBusy.value = true
  try {
    const tags = invTags.value
      .split(',')
      .map(t => t.trim())
      .filter(Boolean)
    const res = await api.invalidate(tenantId.value, {
      db: invDb.value.trim() || undefined,
      coll: invColl.value.trim() || undefined,
      tags: tags.length ? tags : undefined,
    })
    toast.success(`Invalidated (tenant=${res.tenantId}${res.db ? `, db=${res.db}` : ''}${res.coll ? `, coll=${res.coll}` : ''})`)
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    invBusy.value = false
  }
}

async function sendDeleteCode() {
  if (!deleteAck.value) {
    toast.error('Confirm that you understand data will be permanently lost')
    return
  }
  deleteBusy.value = true
  try {
    const res = await api.requestDeleteOrg(tenantId.value)
    deleteStep.value = 'code'
    toast.message((res as { message?: string }).message || 'Verification code sent to your email')
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    deleteBusy.value = false
  }
}

async function confirmDeleteOrg() {
  if (!deleteCode.value.trim()) {
    toast.error('Enter the verification code from your email')
    return
  }
  deleteBusy.value = true
  try {
    await api.confirmDeleteOrg(tenantId.value, deleteCode.value.trim())
    toast.success('Organization deleted')
    await navigateTo('/')
  }
  catch (e) {
    toast.error(api.apiErrorMessage(e))
  }
  finally {
    deleteBusy.value = false
  }
}
</script>

<template>
  <div class="page-shell flex flex-col gap-6">
    <!-- Header -->
    <div class="flex flex-col gap-3">
      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            <BreadcrumbLink as-child>
              <NuxtLink to="/">Organizations</NuxtLink>
            </BreadcrumbLink>
          </BreadcrumbItem>
          <BreadcrumbSeparator />
          <BreadcrumbItem>
            <BreadcrumbPage>{{ tenant?.name || tenantId }}</BreadcrumbPage>
          </BreadcrumbItem>
        </BreadcrumbList>
      </Breadcrumb>

      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="flex flex-col gap-1">
          <p class="wire-label">Organization</p>
          <h1 class="text-2xl font-semibold tracking-tight">
            {{ tenant?.name || tenantId }}
          </h1>
          <p class="font-mono text-xs text-muted-foreground">{{ tenantId }}</p>
        </div>
        <div v-if="tenant" class="flex flex-wrap items-center gap-2">
          <Badge v-if="tenant.role" :variant="roleBadgeVariant(tenant.role)">
            {{ tenant.role }}
          </Badge>
          <Badge :variant="statusBadgeVariant(tenant.status)">{{ tenant.status }}</Badge>
        </div>
      </div>
    </div>

    <Alert v-if="error" variant="destructive">
      <AlertTitle>Could not load organization</AlertTitle>
      <AlertDescription>{{ error }}</AlertDescription>
    </Alert>

    <div v-if="loading" class="flex flex-col gap-3">
      <Skeleton class="h-10 w-full" />
      <Skeleton class="h-64 w-full" />
    </div>

    <template v-else-if="tenant">
      <Alert v-if="isReadOnly">
        <AlertTitle>View-only access</AlertTitle>
        <AlertDescription>
          You are a <strong>member</strong>. Admins manage connections and settings; only an
          <strong>owner</strong> can delete the organization.
        </AlertDescription>
      </Alert>

      <!-- Primary navigation: product vs org admin -->
      <Tabs v-model="area" class="w-full flex-col gap-5">
        <TabsList class="h-auto w-full flex-wrap justify-start gap-1 bg-muted/50 p-1">
          <TabsTrigger value="connections" class="gap-1.5">
            <CableIcon class="size-3.5 opacity-80" />
            Connections
          </TabsTrigger>
          <TabsTrigger value="caching" class="gap-1.5">
            <DatabaseIcon class="size-3.5 opacity-80" />
            Caching
          </TabsTrigger>
          <TabsTrigger value="members" class="gap-1.5">
            <UsersIcon class="size-3.5 opacity-80" />
            Members
          </TabsTrigger>
          <TabsTrigger value="org" class="gap-1.5">
            <SettingsIcon class="size-3.5 opacity-80" />
            Organization
          </TabsTrigger>
        </TabsList>

        <!-- ========== CONNECTIONS WORKSPACE ========== -->
        <TabsContent value="connections" class="mt-0 flex flex-col gap-0">
          <div class="grid gap-4 lg:grid-cols-[minmax(14rem,18rem)_minmax(0,1fr)] lg:items-start">
            <!-- Connection picker rail -->
            <aside class="flex flex-col gap-3 rounded-xl border border-border/80 bg-card/40 p-3">
              <div class="flex items-center justify-between gap-2 px-1">
                <div>
                  <p class="text-sm font-medium">Connections</p>
                  <p class="text-xs text-muted-foreground">
                    Select one to manage
                  </p>
                </div>
                <Button
                  v-if="canManage"
                  size="sm"
                  variant="outline"
                  class="shrink-0"
                  @click="onToggleAddConnection"
                >
                  <PlusIcon data-icon="inline-start" />
                  Add
                </Button>
              </div>

              <div v-if="connectionsLoading" class="flex flex-col gap-2 px-1 py-2">
                <Skeleton class="h-12 w-full" />
                <Skeleton class="h-12 w-full" />
              </div>

              <Empty
                v-else-if="!connections.length && !showAddConnection"
                class="border border-dashed py-8"
              >
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <CableIcon />
                  </EmptyMedia>
                  <EmptyTitle>No connections</EmptyTitle>
                  <EmptyDescription>
                    Add a source MongoDB to get proxy access URIs for your apps.
                  </EmptyDescription>
                </EmptyHeader>
                <Button v-if="canManage" class="mt-2" @click="showAddConnection = true">
                  <PlusIcon data-icon="inline-start" />
                  Add connection
                </Button>
              </Empty>

              <nav v-else class="flex flex-col gap-1" aria-label="Connection list">
                <button
                  v-for="c in connections"
                  :key="c.id"
                  type="button"
                  class="flex flex-col gap-1 rounded-lg border px-3 py-2.5 text-left transition-colors"
                  :class="!showAddConnection && c.id === selectedConnectionId
                    ? 'border-primary/60 bg-primary/10 shadow-[inset_0_0_0_1px] shadow-primary/20'
                    : 'border-transparent bg-transparent hover:bg-muted/50'"
                  @click="selectConnection(c.id)"
                >
                  <span class="flex items-center justify-between gap-2">
                    <span class="truncate text-sm font-medium">{{ c.name }}</span>
                    <Badge
                      v-if="c.autoInvalidateOnWrite"
                      variant="outline"
                      class="shrink-0 text-[10px]"
                    >
                      write-flush
                    </Badge>
                  </span>
                  <span class="truncate font-mono text-[10px] text-muted-foreground">{{ c.id }}</span>
                </button>
              </nav>
            </aside>

            <!-- Main panel -->
            <div class="flex min-w-0 flex-col gap-4">
              <!-- Add connection form -->
              <Card v-if="showAddConnection && canManage">
                <CardHeader>
                  <CardTitle>New connection</CardTitle>
                  <CardDescription>
                    A named source MongoDB for this organization. The URI is encrypted and never shown again.
                  </CardDescription>
                </CardHeader>
                <CardContent class="flex flex-col gap-4">
                  <div class="grid gap-3 sm:grid-cols-2">
                    <Field class="gap-1.5">
                      <FieldLabel for="new-conn-name">Name</FieldLabel>
                      <Input
                        id="new-conn-name"
                        v-model="newConnName"
                        placeholder="prod, staging, analytics…"
                        :disabled="connectionBusy"
                      />
                    </Field>
                    <Field class="gap-1.5 sm:col-span-2">
                      <FieldLabel for="new-conn-uri">Source MongoDB URI</FieldLabel>
                      <Input
                        id="new-conn-uri"
                        v-model="newConnUri"
                        class="font-mono"
                        type="password"
                        placeholder="mongodb://user:pass@host:27017/?…"
                        autocomplete="off"
                        :disabled="connectionBusy"
                      />
                      <FieldDescription>
                        Apps will not use this URI — they use a proxy URI you create under Access.
                      </FieldDescription>
                    </Field>
                  </div>
                </CardContent>
                <CardFooter class="flex flex-wrap gap-2 border-t border-border/60 pt-4">
                  <Button :disabled="connectionBusy" @click="createConnection">
                    <Spinner v-if="connectionBusy" data-icon="inline-start" />
                    Create connection
                  </Button>
                  <Button variant="ghost" :disabled="connectionBusy" @click="showAddConnection = false">
                    Cancel
                  </Button>
                </CardFooter>
              </Card>

              <!-- Empty selection -->
              <Empty
                v-else-if="!selectedConnection"
                class="min-h-64 border border-dashed py-12"
              >
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <CableIcon />
                  </EmptyMedia>
                  <EmptyTitle>Select a connection</EmptyTitle>
                  <EmptyDescription>
                    Choose a connection from the list to manage its source URI, proxy access, and write-cache behavior.
                  </EmptyDescription>
                </EmptyHeader>
              </Empty>

              <!-- Selected connection workspace -->
              <template v-else>
                <div class="flex flex-wrap items-start justify-between gap-3 rounded-xl border border-border/80 bg-card/30 px-4 py-3">
                  <div class="flex min-w-0 flex-col gap-0.5">
                    <div class="flex flex-wrap items-center gap-2">
                      <h2 class="truncate text-lg font-semibold tracking-tight">
                        {{ selectedConnection.name }}
                      </h2>
                      <Badge v-if="selectedConnection.lastValidatedAt" variant="default">
                        validated
                      </Badge>
                      <Badge v-else variant="secondary">
                        not tested
                      </Badge>
                      <Badge
                        v-if="selectedConnection.autoInvalidateOnWrite"
                        variant="outline"
                      >
                        auto-invalidate on
                      </Badge>
                    </div>
                    <p class="font-mono text-xs text-muted-foreground">
                      {{ selectedConnection.id }}
                      <span v-if="selectedConnection.lastValidatedAt">
                        · last validated {{ formatDate(selectedConnection.lastValidatedAt) }}
                      </span>
                    </p>
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    :disabled="connectionBusy"
                    @click="testSelectedConnection"
                  >
                    <Spinner v-if="connectionBusy" data-icon="inline-start" />
                    Test source
                  </Button>
                </div>

                <Tabs v-model="connPanel" class="w-full flex-col gap-4">
                  <TabsList class="h-auto w-full flex-wrap justify-start gap-1 bg-muted/40 p-1">
                    <TabsTrigger value="access">Proxy access</TabsTrigger>
                    <TabsTrigger value="source">Source database</TabsTrigger>
                    <TabsTrigger value="cache-write">Cache on write</TabsTrigger>
                  </TabsList>

                  <!-- Proxy access (primary product surface) -->
                  <TabsContent value="access" class="mt-0 flex flex-col gap-4">
                    <Card v-if="canManage">
                      <CardHeader>
                        <CardTitle class="flex items-center gap-2 text-base">
                          <KeyRoundIcon class="size-4 text-primary" />
                          Create access
                        </CardTitle>
                        <CardDescription>
                          Issue a proxy connection URI for apps. Username is this organization id;
                          traffic routes to <strong>{{ selectedConnection.name }}</strong>.
                          The full URI is shown only once.
                        </CardDescription>
                      </CardHeader>
                      <CardContent class="flex flex-col gap-4">
                        <div class="flex flex-wrap items-end gap-3">
                          <Field class="min-w-48 flex-1 gap-1.5">
                            <FieldLabel>Label (optional)</FieldLabel>
                            <Input
                              v-model="tokenDesc"
                              placeholder="ci-bot, local-dev…"
                              :disabled="tokenBusy"
                            />
                          </Field>
                          <Button :disabled="tokenBusy" @click="issueToken">
                            <Spinner v-if="tokenBusy" data-icon="inline-start" />
                            {{ tokenBusy ? 'Creating…' : 'Create access' }}
                          </Button>
                        </div>
                        <div v-if="issuedToken" class="token-reveal">
                          <p class="wire-label text-amber-500">
                            Proxy connection URI — copy now, shown only once
                          </p>
                          <code class="block break-all">{{ issuedProxyUri() }}</code>
                          <div class="mt-3 flex flex-wrap gap-2">
                            <Button variant="outline" size="sm" @click="copyText(issuedProxyUri())">
                              <CopyIcon data-icon="inline-start" />
                              Copy proxy URI
                            </Button>
                            <Button variant="ghost" size="sm" @click="copyText(issuedToken!.rawToken)">
                              Copy raw token
                            </Button>
                          </div>
                        </div>
                      </CardContent>
                    </Card>
                    <Alert v-else>
                      <KeyRoundIcon />
                      <AlertTitle>Read-only</AlertTitle>
                      <AlertDescription>
                        Members can view credentials; only admins and owners can create or revoke them.
                      </AlertDescription>
                    </Alert>

                    <Card class="overflow-hidden p-0">
                      <CardHeader class="border-b border-border/60 px-6 py-4">
                        <CardTitle class="text-base">Active credentials</CardTitle>
                        <CardDescription>
                          Each row is one proxy secret for this connection. Secrets cannot be shown again after creation.
                        </CardDescription>
                      </CardHeader>
                      <Empty v-if="!tokens.length" class="py-10">
                        <EmptyHeader>
                          <EmptyMedia variant="icon">
                            <KeyRoundIcon />
                          </EmptyMedia>
                          <EmptyTitle>No credentials yet</EmptyTitle>
                          <EmptyDescription>
                            Create access to copy a ready-to-use proxy URI for your apps.
                          </EmptyDescription>
                        </EmptyHeader>
                      </Empty>
                      <Table v-else>
                        <TableHeader>
                          <TableRow class="hover:bg-transparent">
                            <TableHead>ID</TableHead>
                            <TableHead>Label</TableHead>
                            <TableHead>Created</TableHead>
                            <TableHead>Status</TableHead>
                            <TableHead class="w-24" />
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          <TableRow v-for="tok in tokens" :key="tok.id">
                            <TableCell class="font-mono text-xs">{{ tok.id }}</TableCell>
                            <TableCell>{{ tok.description || '—' }}</TableCell>
                            <TableCell class="text-sm text-muted-foreground">
                              {{ formatDate(tok.created_at) }}
                            </TableCell>
                            <TableCell>
                              <Badge v-if="tok.revoked_at" variant="destructive">revoked</Badge>
                              <Badge v-else>active</Badge>
                            </TableCell>
                            <TableCell>
                              <Button
                                v-if="!tok.revoked_at && canManage"
                                variant="destructive"
                                size="sm"
                                @click="confirmRevoke(tok.id)"
                              >
                                Revoke
                              </Button>
                            </TableCell>
                          </TableRow>
                        </TableBody>
                      </Table>
                    </Card>
                  </TabsContent>

                  <!-- Source database -->
                  <TabsContent value="source" class="mt-0 flex flex-col gap-4">
                    <Card>
                      <CardHeader>
                        <CardTitle class="text-base">Source MongoDB</CardTitle>
                        <CardDescription>
                          Real cluster URI for <strong>{{ selectedConnection.name }}</strong>.
                          Encrypted at rest; never returned by the API after save.
                        </CardDescription>
                      </CardHeader>
                      <CardContent v-if="canManage" class="flex flex-col gap-4">
                        <Field class="gap-1.5">
                          <FieldLabel for="update-uri">Replace source URI</FieldLabel>
                          <Input
                            id="update-uri"
                            v-model="updateUri"
                            class="font-mono"
                            type="password"
                            placeholder="mongodb://…"
                            autocomplete="off"
                            :disabled="connectionBusy"
                          />
                          <FieldDescription>
                            Leave blank unless rotating credentials. Use Test to verify the currently stored URI.
                          </FieldDescription>
                        </Field>
                      </CardContent>
                      <CardContent v-else>
                        <p class="text-sm text-muted-foreground">
                          Members can test connectivity but cannot change the source URI.
                        </p>
                      </CardContent>
                      <CardFooter class="flex flex-wrap gap-2 border-t border-border/60 pt-4">
                        <Button
                          v-if="canManage"
                          :disabled="connectionBusy || !updateUri.trim()"
                          @click="saveConnectionUri"
                        >
                          Save new URI
                        </Button>
                        <Button variant="outline" :disabled="connectionBusy" @click="testSelectedConnection">
                          Test connection
                        </Button>
                        <Button
                          v-if="canManage"
                          variant="destructive"
                          :disabled="connectionBusy"
                          @click="confirmDeleteConnection(selectedConnection.id)"
                        >
                          <Trash2Icon data-icon="inline-start" />
                          Delete connection
                        </Button>
                      </CardFooter>
                    </Card>
                  </TabsContent>

                  <!-- Cache on write -->
                  <TabsContent value="cache-write" class="mt-0 flex flex-col gap-4">
                    <Card>
                      <CardHeader>
                        <CardTitle class="text-base">Auto-invalidate on write</CardTitle>
                        <CardDescription>
                          When off (default), cached <code class="font-mono text-xs">*_cache</code> reads
                          expire by TTL only. When on, a successful write through the proxy flushes
                          all cached queries for that collection on this connection.
                        </CardDescription>
                      </CardHeader>
                      <CardContent class="flex flex-col gap-4">
                        <div class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border/70 bg-muted/20 px-3 py-3">
                          <div>
                            <p class="text-sm font-medium">Status for {{ selectedConnection.name }}</p>
                            <p class="text-xs text-muted-foreground">
                              {{ selectedConnection.autoInvalidateOnWrite ? 'Writes flush collection cache' : 'TTL and manual invalidate only' }}
                            </p>
                          </div>
                          <div class="flex items-center gap-2">
                            <Badge :variant="selectedConnection.autoInvalidateOnWrite ? 'default' : 'secondary'">
                              {{ selectedConnection.autoInvalidateOnWrite ? 'enabled' : 'disabled' }}
                            </Badge>
                            <template v-if="canManage">
                              <Button
                                v-if="!selectedConnection.autoInvalidateOnWrite"
                                size="sm"
                                :disabled="connectionBusy"
                                @click="setAutoInvalidateOnWrite(true)"
                              >
                                Enable
                              </Button>
                              <Button
                                v-else
                                size="sm"
                                variant="outline"
                                :disabled="connectionBusy"
                                @click="setAutoInvalidateOnWrite(false)"
                              >
                                Disable
                              </Button>
                            </template>
                          </div>
                        </div>

                        <Alert variant="destructive">
                          <AlertTriangleIcon />
                          <AlertTitle>Performance warning</AlertTitle>
                          <AlertDescription class="flex flex-col gap-2 text-sm">
                            <p>
                              Every successful write flushes <strong>all</strong> cached query shapes for that
                              <code class="font-mono text-xs">db.collection</code> on this connection — not only
                              the documents you changed.
                            </p>
                            <p>
                              <strong>Example:</strong>
                              writing one order per second while dashboards run
                              <code class="font-mono text-xs">db.orders_cache.find(&#123; status: "open" &#125;)</code>
                              will clear that cache on every write. The next read misses Mongo, re-fills Redis,
                              then the next write clears it again — continuous thrash and little cache benefit.
                            </p>
                            <p>
                              Prefer the default (TTL + manual invalidate under Organization) for high write rates.
                              Enable when freshness after writes matters more than write amplification.
                            </p>
                          </AlertDescription>
                        </Alert>
                      </CardContent>
                    </Card>
                  </TabsContent>
                </Tabs>
              </template>
            </div>
          </div>
        </TabsContent>

        <!-- ========== CACHING (org-wide) ========== -->
        <TabsContent value="caching" class="mt-0 flex flex-col gap-4">
          <Alert>
            <DatabaseIcon />
            <AlertTitle>Organization-wide cache policy</AlertTitle>
            <AlertDescription>
              TTL defaults and overrides apply to every connection in this organization.
              Clients opt in per query with a <code class="font-mono text-xs">_cache</code> collection suffix.
            </AlertDescription>
          </Alert>

          <Card class="border-primary/25 bg-primary/5">
            <CardHeader>
              <CardTitle class="text-base">
                Opt-in with <code class="font-mono text-sm">_cache</code>
              </CardTitle>
              <CardDescription>
                <code class="font-mono text-xs">db.orders_cache.find(…)</code> is cached;
                <code class="font-mono text-xs">db.orders.find(…)</code> always hits MongoDB.
              </CardDescription>
            </CardHeader>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle class="text-base">Default TTL</CardTitle>
              <CardDescription>
                Applied to all <code class="font-mono text-xs">*_cache</code> queries unless a per-collection override is set.
              </CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
              <div class="grid gap-3 sm:grid-cols-[minmax(0,16rem)_auto] sm:items-end">
                <Field class="gap-1.5">
                  <FieldLabel for="default-ttl">Default TTL (seconds)</FieldLabel>
                  <Input
                    id="default-ttl"
                    v-model.number="defaultTtl"
                    type="number"
                    min="1"
                    step="1"
                    :disabled="isReadOnly || defaultsBusy"
                  />
                </Field>
                <Button
                  v-if="canManage"
                  class="w-full sm:w-auto"
                  :disabled="defaultsBusy || isReadOnly"
                  @click="saveDefaults"
                >
                  <Spinner v-if="defaultsBusy" data-icon="inline-start" />
                  {{ defaultsBusy ? 'Saving…' : 'Save default TTL' }}
                </Button>
              </div>
              <p v-if="policy" class="text-xs text-muted-foreground">
                Active default: <strong class="text-foreground">{{ policy.defaultTtlSeconds }}s</strong>
                · key version {{ policy.cacheKeyVersion }}
                · updated {{ formatDate(policy.updatedAt) }}
              </p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle class="text-base">Per-collection overrides</CardTitle>
              <CardDescription>
                Use the real collection name (<code class="font-mono text-xs">db.orders</code>, not
                <code class="font-mono text-xs">db.orders_cache</code>).
              </CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
              <Empty v-if="!collectionEntries.length" class="border border-dashed py-8">
                <EmptyHeader>
                  <EmptyTitle>No overrides</EmptyTitle>
                  <EmptyDescription>
                    All <code class="font-mono text-xs">*_cache</code> queries use the default TTL.
                  </EmptyDescription>
                </EmptyHeader>
              </Empty>

              <div v-else class="overflow-hidden rounded-lg border">
                <Table>
                  <TableHeader>
                    <TableRow class="hover:bg-transparent">
                      <TableHead>Real collection</TableHead>
                      <TableHead>Client uses</TableHead>
                      <TableHead>TTL (s)</TableHead>
                      <TableHead>Max result bytes</TableHead>
                      <TableHead class="w-32" />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    <TableRow v-for="row in collectionEntries" :key="row.key">
                      <TableCell class="font-mono text-xs">{{ row.key }}</TableCell>
                      <TableCell class="font-mono text-xs text-muted-foreground">{{ row.key }}_cache</TableCell>
                      <TableCell>
                        <strong>{{ effectiveTtl(row) }}</strong>
                        <span v-if="!row.ttlSeconds || row.ttlSeconds <= 0" class="text-xs text-muted-foreground"> (default)</span>
                      </TableCell>
                      <TableCell class="text-muted-foreground">
                        {{ row.maxResultBytes ?? 'default (1 MiB)' }}
                      </TableCell>
                      <TableCell>
                        <Button
                          v-if="canManage"
                          variant="ghost"
                          size="sm"
                          :disabled="collBusy"
                          @click="removeCollectionOverride(row.key)"
                        >
                          Use default
                        </Button>
                      </TableCell>
                    </TableRow>
                  </TableBody>
                </Table>
              </div>

              <template v-if="canManage">
                <div class="flex flex-col gap-3 border-t border-border/60 pt-4">
                  <p class="wire-label">Add / update override</p>
                  <div
                    class="grid gap-3 sm:grid-cols-2 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,7rem)_minmax(0,9rem)_auto] lg:items-end"
                  >
                    <Field class="gap-1.5 sm:col-span-2 lg:col-span-1">
                      <FieldLabel for="new-coll-key">Real db.collection</FieldLabel>
                      <Input
                        id="new-coll-key"
                        v-model="newCollKey"
                        class="font-mono"
                        placeholder="mydb.orders"
                        :disabled="collBusy"
                      />
                    </Field>
                    <Field class="gap-1.5">
                      <FieldLabel for="new-coll-ttl">TTL (s)</FieldLabel>
                      <Input
                        id="new-coll-ttl"
                        v-model.number="newCollTtl"
                        type="number"
                        min="1"
                        :placeholder="String(defaultTtl)"
                        :disabled="collBusy"
                      />
                    </Field>
                    <Field class="gap-1.5">
                      <FieldLabel for="new-coll-max">Max bytes</FieldLabel>
                      <Input
                        id="new-coll-max"
                        v-model.number="newCollMaxBytes"
                        type="number"
                        min="1"
                        placeholder="1048576"
                        :disabled="collBusy"
                      />
                    </Field>
                    <Button class="w-full lg:w-auto" :disabled="collBusy" @click="addCollection">
                      <Spinner v-if="collBusy" data-icon="inline-start" />
                      Save override
                    </Button>
                  </div>
                </div>
              </template>
            </CardContent>
          </Card>

          <Card v-if="canManage">
            <CardHeader>
              <CardTitle class="text-base">Manual invalidate</CardTitle>
              <CardDescription>
                Flush cache for a real collection or tags across all connections in this organization.
              </CardDescription>
            </CardHeader>
            <CardContent class="grid gap-3 sm:grid-cols-3">
              <Field class="gap-1.5">
                <FieldLabel>Database</FieldLabel>
                <Input v-model="invDb" class="font-mono" placeholder="mydb" :disabled="invBusy" />
              </Field>
              <Field class="gap-1.5">
                <FieldLabel>Collection</FieldLabel>
                <Input v-model="invColl" class="font-mono" placeholder="orders" :disabled="invBusy" />
              </Field>
              <Field class="gap-1.5">
                <FieldLabel>Tags (comma-separated)</FieldLabel>
                <Input v-model="invTags" placeholder="optional" :disabled="invBusy" />
              </Field>
            </CardContent>
            <CardFooter class="border-t border-border/60 pt-4">
              <Button :disabled="invBusy" @click="runInvalidate">
                <Spinner v-if="invBusy" data-icon="inline-start" />
                Invalidate
              </Button>
            </CardFooter>
          </Card>
        </TabsContent>

        <!-- ========== MEMBERS ========== -->
        <TabsContent value="members" class="mt-0">
          <Card>
            <CardHeader>
              <CardTitle class="flex items-center gap-2">
                <UsersIcon class="size-4 text-primary" />
                Team
              </CardTitle>
              <CardDescription>
                <strong>member</strong> — read-only ·
                <strong>admin</strong> — manage connections and settings ·
                <strong>owner</strong> — full control including deletion.
              </CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-6">
              <template v-if="canManage">
                <div class="grid gap-3 sm:grid-cols-2">
                  <Field class="gap-1.5">
                    <FieldLabel>Email</FieldLabel>
                    <Input v-model="inviteEmail" type="email" placeholder="teammate@company.com" :disabled="membersBusy" />
                  </Field>
                  <Field class="gap-1.5">
                    <FieldLabel>Role</FieldLabel>
                    <Select v-model="inviteRole" :disabled="membersBusy">
                      <SelectTrigger class="w-full">
                        <SelectValue placeholder="Select role" />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectGroup>
                          <SelectItem
                            v-for="opt in inviteRoleOptions"
                            :key="opt.value"
                            :value="opt.value"
                          >
                            {{ opt.label }}
                          </SelectItem>
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                  </Field>
                </div>
                <div>
                  <Button :disabled="membersBusy || !inviteEmail.trim()" @click="sendInvite">
                    <Spinner v-if="membersBusy" data-icon="inline-start" />
                    {{ membersBusy ? 'Working…' : 'Send invite' }}
                  </Button>
                </div>
              </template>
              <p v-else class="text-sm text-muted-foreground">
                Only admins and owners can invite or remove members.
              </p>

              <div class="flex flex-col gap-2">
                <p class="wire-label">Members</p>
                <div class="overflow-hidden rounded-lg border">
                  <Table>
                    <TableHeader>
                      <TableRow class="hover:bg-transparent">
                        <TableHead>Email</TableHead>
                        <TableHead>Name</TableHead>
                        <TableHead>Role</TableHead>
                        <TableHead class="w-24" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      <TableRow v-for="m in members" :key="m.userId">
                        <TableCell>{{ m.email || m.userId }}</TableCell>
                        <TableCell>{{ m.name || '—' }}</TableCell>
                        <TableCell>
                          <Badge :variant="roleBadgeVariant(m.role)">{{ m.role }}</Badge>
                        </TableCell>
                        <TableCell>
                          <Button
                            v-if="canManage"
                            variant="destructive"
                            size="sm"
                            :disabled="membersBusy"
                            @click="onRemoveMember(m.userId)"
                          >
                            Remove
                          </Button>
                        </TableCell>
                      </TableRow>
                    </TableBody>
                  </Table>
                </div>
              </div>

              <div v-if="pendingInvites.length && canManage" class="flex flex-col gap-2">
                <p class="wire-label">Pending invites</p>
                <div class="overflow-hidden rounded-lg border">
                  <Table>
                    <TableHeader>
                      <TableRow class="hover:bg-transparent">
                        <TableHead>Email</TableHead>
                        <TableHead>Role</TableHead>
                        <TableHead>Expires</TableHead>
                        <TableHead class="w-24" />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      <TableRow v-for="inv in pendingInvites" :key="inv.id">
                        <TableCell>{{ inv.email }}</TableCell>
                        <TableCell>
                          <Badge :variant="roleBadgeVariant(inv.role)">{{ inv.role }}</Badge>
                        </TableCell>
                        <TableCell class="text-sm text-muted-foreground">{{ formatDate(inv.expires_at) }}</TableCell>
                        <TableCell>
                          <Button
                            variant="ghost"
                            size="sm"
                            :disabled="membersBusy"
                            @click="onRevokeInvite(inv.id)"
                          >
                            Revoke
                          </Button>
                        </TableCell>
                      </TableRow>
                    </TableBody>
                  </Table>
                </div>
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <!-- ========== ORGANIZATION SETTINGS ========== -->
        <TabsContent value="org" class="mt-0 flex flex-col gap-4">
          <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Card size="sm">
              <CardHeader class="pb-1">
                <p class="wire-label">Tenant ID</p>
              </CardHeader>
              <CardContent>
                <p class="truncate font-mono text-sm font-medium">{{ tenant.id }}</p>
              </CardContent>
            </Card>
            <Card size="sm">
              <CardHeader class="pb-1">
                <p class="wire-label">Status</p>
              </CardHeader>
              <CardContent>
                <p class="text-sm font-medium capitalize">{{ tenant.status }}</p>
              </CardContent>
            </Card>
            <Card size="sm">
              <CardHeader class="pb-1">
                <p class="wire-label">Created</p>
              </CardHeader>
              <CardContent>
                <p class="text-sm font-medium">{{ formatDate(tenant.created_at) }}</p>
              </CardContent>
            </Card>
            <Card size="sm">
              <CardHeader class="pb-1">
                <p class="wire-label">Your role</p>
              </CardHeader>
              <CardContent>
                <Badge v-if="tenant.role" :variant="roleBadgeVariant(tenant.role)">{{ tenant.role }}</Badge>
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle class="flex items-center gap-2 text-base">
                <ShieldIcon class="size-4 text-primary" />
                How this organization works
              </CardTitle>
            </CardHeader>
            <CardContent class="flex flex-col gap-2 text-sm text-muted-foreground">
              <p>
                <strong class="text-foreground">Connections</strong> hold source Mongo URIs and proxy credentials.
                Pick a connection to issue app URIs.
              </p>
              <p>
                <strong class="text-foreground">Caching</strong> is org-wide TTL policy. Clients opt in with
                <code class="font-mono text-xs">_cache</code> on collection names.
              </p>
              <p>
                <strong class="text-foreground">Members</strong> control who can manage this org.
              </p>
            </CardContent>
            <CardFooter class="flex flex-wrap gap-2 border-t border-border/60 pt-4">
              <Button variant="outline" size="sm" @click="area = 'connections'">
                Open connections
              </Button>
              <Button variant="outline" size="sm" @click="area = 'caching'">
                Open caching
              </Button>
              <Button variant="outline" size="sm" @click="area = 'members'">
                Open members
              </Button>
            </CardFooter>
          </Card>

          <Card v-if="canDelete" class="border-destructive/40">
            <CardHeader>
              <CardTitle class="text-base text-destructive">Danger zone</CardTitle>
              <CardDescription>
                Permanently delete this organization and
                <strong>all related data</strong>: members, invites, connections, cache policies,
                proxy credentials, and audit history.
              </CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
              <template v-if="deleteStep === 'warn'">
                <label class="flex items-start gap-2 text-sm">
                  <Checkbox v-model="deleteAck" class="mt-0.5" />
                  <span>I understand this cannot be undone and all data will be lost.</span>
                </label>
                <Button variant="destructive" :disabled="deleteBusy || !deleteAck" @click="sendDeleteCode">
                  <Spinner v-if="deleteBusy" data-icon="inline-start" />
                  Send verification code
                </Button>
              </template>
              <template v-else>
                <Field class="gap-1.5 max-w-xs">
                  <FieldLabel>Email verification code</FieldLabel>
                  <Input v-model="deleteCode" placeholder="6-digit code" :disabled="deleteBusy" />
                </Field>
                <div class="flex flex-wrap gap-2">
                  <Button variant="destructive" :disabled="deleteBusy" @click="confirmDeleteOrg">
                    <Spinner v-if="deleteBusy" data-icon="inline-start" />
                    Confirm permanent delete
                  </Button>
                  <Button variant="ghost" :disabled="deleteBusy" @click="deleteStep = 'warn'; deleteCode = ''">
                    Back
                  </Button>
                </div>
              </template>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </template>

    <AlertDialog v-model:open="revokeOpen">
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Revoke this proxy access?</AlertDialogTitle>
          <AlertDialogDescription>
            Clients using this connection URI will fail auth immediately. This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction variant="destructive" @click="revokeToken">
            Revoke access
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>

    <AlertDialog v-model:open="deleteConnOpen">
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete this connection?</AlertDialogTitle>
          <AlertDialogDescription>
            The source URI and all proxy access credentials for this connection will be removed permanently.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction variant="destructive" @click="deleteConnection">
            Delete connection
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </div>
</template>
