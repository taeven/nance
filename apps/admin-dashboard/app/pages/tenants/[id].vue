<script setup lang="ts">
import {
  AlertTriangleIcon,
  CopyIcon,
  KeyRoundIcon,
  Trash2Icon,
} from '@lucide/vue'
import { toast } from 'vue-sonner'
import type {
  CachePolicy,
  CollectionPolicy,
  Connection,
  IssueTokenResponse,
  OrganizationInvite,
  OrganizationMember,
  SavingsReport,
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
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
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
const api = useAcceleratorApi()
const tenantId = computed(() => String(route.params.id || ''))

const tab = ref('overview')

const myRole = computed(() => tenant.value?.role || 'member')
const canManage = computed(() => tenant.value?.canManage === true || myRole.value === 'owner' || myRole.value === 'admin')
const canDelete = computed(() => tenant.value?.canDelete === true || myRole.value === 'owner')
const isReadOnly = computed(() => !canManage.value)

const tenant = ref<Tenant | null>(null)
const policy = ref<CachePolicy | null>(null)
const tokens = ref<Token[]>([])
const savings = ref<SavingsReport | null>(null)
const loading = ref(true)
const error = ref('')

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

const selectedConnection = computed(() =>
  connections.value.find(c => c.id === selectedConnectionId.value) || null,
)

async function loadConnections() {
  try {
    connections.value = await api.listConnections(tenantId.value)
    if (selectedConnectionId.value && !connections.value.some(c => c.id === selectedConnectionId.value)) {
      selectedConnectionId.value = null
      tokens.value = []
    }
    if (!selectedConnectionId.value && connections.value.length) {
      selectedConnectionId.value = connections.value[0].id
    }
    if (selectedConnectionId.value) {
      await loadTokens()
    }
  }
  catch (e) {
    toast.error(`Connections: ${api.apiErrorMessage(e)}`)
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

async function loadConnectionTab() {
  await loadConnections()
}

async function selectConnection(id: string) {
  selectedConnectionId.value = id
  issuedToken.value = null
  updateUri.value = ''
  tokenDesc.value = ''
  await loadTokens()
}

async function loadSavings() {
  try {
    savings.value = await api.getSavings(tenantId.value)
  }
  catch (e) {
    toast.error(`Savings: ${api.apiErrorMessage(e)}`)
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

watch(tab, async (t) => {
  if (t === 'cache' && !policy.value) await loadPolicy()
  if (t === 'backend') await loadConnectionTab()
  if (t === 'savings') await loadSavings()
  if (t === 'members') await loadMembers()
})

onMounted(async () => {
  await loadTenant()
  await loadPolicy()
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
    toast.success(`Connection “${c.name}” created (URI encrypted at rest).`)
    selectedConnectionId.value = c.id
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
    toast.success('Source URI updated (encrypted). Never shown again via API.')
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
    toast.success(`Default cache TTL set to ${ttl}s for all _cache queries`)
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
    toast.success(`Override saved for ${key} (applies when clients use ${key.split('.').pop()}_cache)`)
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
    toast.error('Use real db.collection format (e.g. mydb.orders), not mydb.orders_cache')
    return
  }
  if (key.endsWith('_cache')) {
    toast.error('Use the real collection name (without _cache). Clients append _cache in queries.')
    return
  }
  const ttl = Number(newCollTtl.value) || Number(defaultTtl.value) || 60
  const pol: CollectionPolicy = {
    enabled: true,
    ttlSeconds: ttl,
  }
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
    toast.message(`${key} will inherit the organization default TTL (${defaultTtl.value}s)`)
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
  const endpoint = '127.0.0.1:27018'
  return `mongodb://${encodeURIComponent(tenantId.value)}:${encodeURIComponent(issued.rawToken)}@${endpoint}/?authMechanism=PLAIN&authSource=$external`
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
            <BreadcrumbPage class="font-mono text-xs">{{ tenantId }}</BreadcrumbPage>
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
            your role: {{ tenant.role }}
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
      <Skeleton class="h-40 w-full" />
    </div>

    <template v-else-if="tenant">
      <Alert v-if="isReadOnly">
        <AlertTitle>View-only access</AlertTitle>
        <AlertDescription>
          You are a <strong>member</strong> — view-only access. Admins manage settings; only an
          <strong>owner</strong> can delete the organization.
        </AlertDescription>
      </Alert>
      <Alert v-else-if="myRole === 'admin'">
        <AlertTitle>Admin access</AlertTitle>
        <AlertDescription>
          You can manage the connection, caching, and members. Only an
          <strong>owner</strong> can delete this organization.
        </AlertDescription>
      </Alert>

      <Tabs v-model="tab" class="w-full flex-col gap-4">
        <TabsList class="h-auto w-full flex-wrap justify-start gap-1 bg-muted/50 p-1">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="backend">Connection</TabsTrigger>
          <TabsTrigger value="cache">Caching</TabsTrigger>
          <TabsTrigger value="members">Members</TabsTrigger>
          <TabsTrigger value="invalidate">Invalidate</TabsTrigger>
          <TabsTrigger value="savings">Savings</TabsTrigger>
          <TabsTrigger
            v-if="canDelete"
            value="danger"
            class="text-destructive data-active:text-destructive"
          >
            Danger zone
          </TabsTrigger>
        </TabsList>

        <!-- Overview -->
        <TabsContent value="overview" class="flex flex-col gap-4">
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
                <p class="wire-label">Updated</p>
              </CardHeader>
              <CardContent>
                <p class="text-sm font-medium">{{ formatDate(tenant.updated_at) }}</p>
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle>How caching works</CardTitle>
              <CardDescription>
                Clients opt in <strong>per query</strong> by using a collection name that ends with
                <code class="font-mono text-xs">_cache</code>. The proxy strips that suffix, reads the real collection,
                and serves results from Redis with a default TTL of
                <strong>{{ policy?.defaultTtlSeconds ?? defaultTtl }} seconds</strong>
                (override under Caching). Without the suffix, every query hits MongoDB.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ul class="flex flex-col gap-2 text-sm text-muted-foreground">
                <li>
                  <code class="font-mono text-xs text-foreground">db.orders_cache.find(…)</code>
                  — cached (real collection: <code class="font-mono text-xs">orders</code>)
                </li>
                <li>
                  <code class="font-mono text-xs text-foreground">db.orders.find(…)</code>
                  — always bypasses cache
                </li>
                <li>
                  Entries expire by <strong class="text-foreground">TTL</strong> (default 60s); use
                  <strong class="text-foreground">Invalidate</strong> for a manual flush — writes do not clear cache automatically
                </li>
              </ul>
            </CardContent>
            <CardFooter class="flex flex-wrap gap-2 border-t border-border/60 pt-4">
              <Button variant="outline" size="sm" @click="tab = 'backend'">
                {{ canManage ? 'Configure connection' : 'View connection' }}
              </Button>
              <Button variant="outline" size="sm" @click="tab = 'cache'">
                {{ canManage ? 'Configure caching' : 'View caching' }}
              </Button>
              <Button v-if="canManage" variant="outline" size="sm" @click="tab = 'backend'">
                Get proxy URI
              </Button>
              <Button variant="outline" size="sm" @click="tab = 'members'">
                {{ canManage ? 'Manage members' : 'View members' }}
              </Button>
              <Button v-if="canManage" variant="outline" size="sm" @click="tab = 'invalidate'">
                Invalidate cache
              </Button>
              <Button v-if="canDelete" variant="destructive" size="sm" @click="tab = 'danger'">
                Delete organization
              </Button>
            </CardFooter>
          </Card>
        </TabsContent>

        <!-- Connections (many source Mongo + proxy access per connection) -->
        <TabsContent value="backend" class="flex flex-col gap-4">
          <Card>
            <CardHeader>
              <CardTitle>Source connections</CardTitle>
              <CardDescription>
                An organization can have multiple named MongoDB sources (e.g. prod, staging).
                Each has its own encrypted URI and proxy access credentials.
                Apps never use the source URI — they use a <strong>proxy connection URI</strong> issued per connection.
              </CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
              <Empty v-if="!connections.length" class="border border-dashed py-8">
                <EmptyHeader>
                  <EmptyTitle>No connections yet</EmptyTitle>
                  <EmptyDescription>
                    Add a source MongoDB connection to start issuing proxy access.
                  </EmptyDescription>
                </EmptyHeader>
              </Empty>

              <div v-else class="flex flex-col gap-2">
                <button
                  v-for="c in connections"
                  :key="c.id"
                  type="button"
                  class="flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3 py-2 text-left transition-colors"
                  :class="c.id === selectedConnectionId
                    ? 'border-primary bg-primary/5'
                    : 'border-border/80 hover:bg-muted/40'"
                  @click="selectConnection(c.id)"
                >
                  <div class="flex flex-col gap-0.5">
                    <span class="text-sm font-medium">{{ c.name }}</span>
                    <span class="font-mono text-xs text-muted-foreground">{{ c.id }}</span>
                  </div>
                  <div class="flex flex-wrap items-center gap-2">
                    <Badge v-if="c.lastValidatedAt" variant="default">validated</Badge>
                    <Badge v-else variant="secondary">not tested</Badge>
                    <span class="text-xs text-muted-foreground">{{ formatDate(c.created_at) }}</span>
                  </div>
                </button>
              </div>

              <template v-if="canManage">
                <div class="flex flex-col gap-3 border-t border-border/60 pt-4">
                  <p class="wire-label">Add connection</p>
                  <div class="grid gap-3 sm:grid-cols-2">
                    <Field class="gap-1.5">
                      <FieldLabel for="new-conn-name">Name</FieldLabel>
                      <Input
                        id="new-conn-name"
                        v-model="newConnName"
                        placeholder="prod, staging, …"
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
                        placeholder="mongodb://user:pass@host:27017/db?…"
                        autocomplete="off"
                        :disabled="connectionBusy"
                      />
                    </Field>
                  </div>
                  <Button class="w-fit" :disabled="connectionBusy" @click="createConnection">
                    <Spinner v-if="connectionBusy" data-icon="inline-start" />
                    {{ connectionBusy ? 'Working…' : 'Add connection' }}
                  </Button>
                </div>
              </template>
            </CardContent>
          </Card>

          <template v-if="selectedConnection">
            <Card>
              <CardHeader>
                <CardTitle>{{ selectedConnection.name }}</CardTitle>
                <CardDescription>
                  Update the encrypted source URI or test connectivity.
                  <span class="font-mono text-xs">{{ selectedConnection.id }}</span>
                </CardDescription>
              </CardHeader>
              <CardContent class="flex flex-col gap-4">
                <div class="flex flex-wrap items-center gap-2 text-sm">
                  <Badge v-if="selectedConnection.lastValidatedAt">
                    last validated {{ formatDate(selectedConnection.lastValidatedAt) }}
                  </Badge>
                  <Badge v-else variant="secondary">not tested yet</Badge>
                </div>
                <Field v-if="canManage" class="gap-1.5">
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
                  <FieldDescription>Leave blank unless rotating the source cluster credentials.</FieldDescription>
                </Field>
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

            <Card v-if="canManage">
              <CardHeader>
                <CardTitle>Proxy access</CardTitle>
                <CardDescription>
                  Credentials for this connection only. Clients use the proxy URI (username = org id);
                  traffic is routed to this source MongoDB.
                </CardDescription>
              </CardHeader>
              <CardContent class="flex flex-col gap-4">
                <div class="flex flex-wrap items-end gap-3">
                  <Field class="min-w-48 flex-1">
                    <FieldLabel>Label (optional)</FieldLabel>
                    <Input
                      v-model="tokenDesc"
                      placeholder="ci-bot, local-dev, …"
                      :disabled="tokenBusy"
                    />
                  </Field>
                  <Button :disabled="tokenBusy" @click="issueToken">
                    <Spinner v-if="tokenBusy" data-icon="inline-start" />
                    {{ tokenBusy ? 'Creating…' : 'Create access' }}
                  </Button>
                </div>

                <div v-if="issuedToken" class="token-reveal">
                  <p class="wire-label text-amber-500">Proxy connection URI — copy now, shown only once</p>
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
              <AlertTitle>Read-only for proxy access</AlertTitle>
              <AlertDescription>
                Members can list credentials; only admins and owners can create or revoke them.
              </AlertDescription>
            </Alert>

            <Card class="overflow-hidden p-0">
              <CardHeader class="border-b border-border/60 px-6 py-4">
                <CardTitle class="text-base">Active credentials — {{ selectedConnection.name }}</CardTitle>
              </CardHeader>
              <Empty v-if="!tokens.length" class="py-10">
                <EmptyHeader>
                  <EmptyMedia variant="icon">
                    <KeyRoundIcon />
                  </EmptyMedia>
                  <EmptyTitle>No proxy access yet</EmptyTitle>
                  <EmptyDescription>
                    Create access to get a proxy connection URI for this source.
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
                    <TableCell class="text-sm text-muted-foreground">{{ formatDate(tok.created_at) }}</TableCell>
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
          </template>
        </TabsContent>

        <!-- Caching -->
        <TabsContent value="cache" class="flex flex-col gap-4">
          <Card class="border-primary/25 bg-primary/5">
            <CardHeader>
              <CardTitle>
                Opt-in with <code class="font-mono text-sm">_cache</code>
              </CardTitle>
              <CardDescription>
                Every collection is eligible for caching. Developers choose per query by appending
                <code class="font-mono text-xs">_cache</code> to the collection name. No policy toggle is required to turn caching on.
              </CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-2">
              <div class="flex flex-wrap items-center gap-2 rounded-lg border border-border/80 bg-background/60 px-3 py-2">
                <Badge>cached</Badge>
                <code class="font-mono text-xs">db.orders_cache.find(&#123; status: "open" &#125;)</code>
                <span class="text-xs text-muted-foreground">
                  → real <code class="font-mono">orders</code> · TTL {{ policy?.defaultTtlSeconds ?? defaultTtl }}s unless overridden
                </span>
              </div>
              <div class="flex flex-wrap items-center gap-2 rounded-lg border border-border/80 bg-background/60 px-3 py-2">
                <Badge variant="secondary">bypass</Badge>
                <code class="font-mono text-xs">db.orders.find(&#123; status: "open" &#125;)</code>
                <span class="text-xs text-muted-foreground">→ always MongoDB, never Redis</span>
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Default TTL</CardTitle>
              <CardDescription>
                Applied to <strong>all</strong> <code class="font-mono text-xs">*_cache</code> queries for this organization
                unless a per-collection override is set below. Platform default is <strong>60 seconds</strong>.
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
              <FieldDescription class="!mt-0">
                Example: 60 caches results for one minute after each miss.
              </FieldDescription>
              <p v-if="!canManage" class="text-sm text-muted-foreground">
                Only admins and owners can change TTL settings.
              </p>
              <p v-if="policy" class="text-xs text-muted-foreground">
                Active default: <strong class="text-foreground">{{ policy.defaultTtlSeconds }}s</strong>
                · Cache key version: {{ policy.cacheKeyVersion }}
                · Updated {{ formatDate(policy.updatedAt) }}
              </p>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Per-collection overrides</CardTitle>
              <CardDescription>
                Optional. Use the <strong>real</strong> collection name
                (<code class="font-mono text-xs">db.orders</code>, not
                <code class="font-mono text-xs">db.orders_cache</code>) to set a different TTL or max cached result size.
                Leave empty to use the organization default ({{ policy?.defaultTtlSeconds ?? defaultTtl }}s).
              </CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
              <Empty v-if="!collectionEntries.length" class="border border-dashed py-8">
                <EmptyHeader>
                  <EmptyTitle>No overrides</EmptyTitle>
                  <EmptyDescription>
                    All <code class="font-mono text-xs">*_cache</code> queries use the default TTL above.
                    Add an override only when a hot collection needs a shorter or longer TTL.
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
                    <Button
                      class="w-full lg:w-auto"
                      :disabled="collBusy"
                      @click="addCollection"
                    >
                      <Spinner v-if="collBusy" data-icon="inline-start" />
                      Save override
                    </Button>
                  </div>
                  <FieldDescription class="!mt-0">
                    Use the real collection name, not <code class="font-mono">mydb.orders_cache</code>.
                  </FieldDescription>
                </div>
              </template>
            </CardContent>
          </Card>
        </TabsContent>

        <!-- Members -->
        <TabsContent value="members">
          <Card>
            <CardHeader>
              <CardTitle>User management</CardTitle>
              <CardDescription>
                <strong>member</strong> — read-only.
                <strong>admin</strong> — manage settings (not delete org).
                <strong>owner</strong> — full control including deletion.
                Invitees sign in with the invited email and accept from Organizations.
              </CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-6">
              <template v-if="canManage">
                <div class="grid gap-3 sm:grid-cols-2">
                  <Field>
                    <FieldLabel>Email</FieldLabel>
                    <Input v-model="inviteEmail" type="email" placeholder="teammate@company.com" :disabled="membersBusy" />
                  </Field>
                  <Field>
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
              <p v-else class="text-sm text-muted-foreground">Members cannot invite or remove users.</p>

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
                        <TableCell class="text-sm text-muted-foreground">{{ inv.expires_at }}</TableCell>
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

        <!-- Invalidate -->
        <TabsContent value="invalidate">
          <Card>
            <CardHeader>
              <CardTitle>Explicit cache invalidation</CardTitle>
              <CardDescription>
                Flush Redis entries for this organization. Use the <strong>real</strong> collection name
                (e.g. <code class="font-mono text-xs">orders</code>), matching what was stored from
                <code class="font-mono text-xs">orders_cache</code> reads.
                The proxy does <strong>not</strong> invalidate on write — only TTL expiry and this explicit action clear cached results.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <FieldGroup>
                <div class="grid gap-3 sm:grid-cols-2">
                  <Field>
                    <FieldLabel>Database (optional)</FieldLabel>
                    <Input v-model="invDb" class="font-mono" placeholder="mydb" :disabled="invBusy" />
                  </Field>
                  <Field>
                    <FieldLabel>Real collection (optional)</FieldLabel>
                    <Input v-model="invColl" class="font-mono" placeholder="orders" :disabled="invBusy" />
                    <FieldDescription>Not <code class="font-mono">orders_cache</code></FieldDescription>
                  </Field>
                </div>
                <Field>
                  <FieldLabel>Tags (optional, comma-separated)</FieldLabel>
                  <Input v-model="invTags" class="font-mono" placeholder="user:1, order:99" :disabled="invBusy" />
                </Field>
              </FieldGroup>
            </CardContent>
            <CardFooter class="border-t border-border/60 pt-4">
              <Button
                v-if="canManage"
                variant="destructive"
                :disabled="invBusy"
                @click="runInvalidate"
              >
                <Spinner v-if="invBusy" data-icon="inline-start" />
                {{ invBusy ? 'Invalidating…' : 'Invalidate cache' }}
              </Button>
              <p v-else class="text-sm text-muted-foreground">Only admins and owners can invalidate cache.</p>
            </CardFooter>
          </Card>
        </TabsContent>

        <!-- Savings -->
        <TabsContent value="savings">
          <Card>
            <CardHeader>
              <CardTitle>Savings / metrics</CardTitle>
            </CardHeader>
            <CardContent>
              <div v-if="!savings" class="flex items-center gap-2 text-sm text-muted-foreground">
                <Spinner />
                Loading…
              </div>
              <template v-else>
                <p class="mb-4 text-sm text-muted-foreground">{{ savings.note }}</p>
                <p class="wire-label mb-2">Suggested Prometheus queries</p>
                <ul class="flex flex-col gap-2">
                  <li
                    v-for="(q, i) in savings.suggestedQueries"
                    :key="i"
                    class="rounded-md border border-border/80 bg-muted/30 px-3 py-2"
                  >
                    <code class="break-all font-mono text-xs">{{ q }}</code>
                  </li>
                </ul>
              </template>
            </CardContent>
          </Card>
        </TabsContent>

        <!-- Danger -->
        <TabsContent v-if="canDelete" value="danger">
          <Card class="border-destructive/40 bg-destructive/5">
            <CardHeader>
              <CardTitle class="flex items-center gap-2 text-destructive">
                <AlertTriangleIcon class="size-4" />
                Delete organization
              </CardTitle>
              <CardDescription>
                Permanently remove <strong>{{ tenant?.name }}</strong>
                (<code class="font-mono text-xs">{{ tenantId }}</code>) and
                <strong>all related data</strong>: members, invites, backend connection, cache policies, proxy tokens, and audit history.
                This cannot be undone. Only <strong>owners</strong> can delete an organization.
              </CardDescription>
            </CardHeader>
            <CardContent class="flex flex-col gap-4">
              <template v-if="deleteStep === 'warn'">
                <label class="flex items-start gap-3 text-sm leading-snug">
                  <Checkbox
                    :model-value="deleteAck"
                    class="mt-0.5"
                    @update:model-value="(v: boolean | 'indeterminate') => deleteAck = v === true"
                  />
                  <span>
                    I understand that all organization data will be permanently lost and cannot be recovered.
                  </span>
                </label>
                <div>
                  <Button
                    variant="destructive"
                    :disabled="deleteBusy || !deleteAck"
                    @click="sendDeleteCode"
                  >
                    <Spinner v-if="deleteBusy" data-icon="inline-start" />
                    {{ deleteBusy ? 'Sending…' : 'Send verification code to my email' }}
                  </Button>
                  <p class="mt-2 text-xs text-muted-foreground">
                    We will email a 6-digit code to your account address. Enter it on the next step to confirm deletion.
                  </p>
                </div>
              </template>

              <template v-else>
                <Alert>
                  <AlertTitle>Check your email</AlertTitle>
                  <AlertDescription>
                    Enter the verification code below to delete this organization forever
                    (also in control plane logs in dev).
                  </AlertDescription>
                </Alert>
                <Field>
                  <FieldLabel>Verification code</FieldLabel>
                  <Input
                    v-model="deleteCode"
                    type="text"
                    inputmode="numeric"
                    autocomplete="one-time-code"
                    placeholder="123456"
                    class="font-mono tracking-widest"
                    :disabled="deleteBusy"
                  />
                </Field>
                <div class="flex flex-wrap gap-2">
                  <Button
                    variant="outline"
                    :disabled="deleteBusy"
                    @click="deleteStep = 'warn'; deleteCode = ''"
                  >
                    Back
                  </Button>
                  <Button
                    variant="destructive"
                    :disabled="deleteBusy || !deleteCode.trim()"
                    @click="confirmDeleteOrg"
                  >
                    <Spinner v-if="deleteBusy" data-icon="inline-start" />
                    <Trash2Icon v-else data-icon="inline-start" />
                    {{ deleteBusy ? 'Deleting…' : 'Permanently delete organization' }}
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
