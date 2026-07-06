import type {
  AuthVerifyResponse,
  CachePolicy,
  CollectionPolicy,
  Connection,
  InvalidateRequest,
  InvalidateResponse,
  IssueTokenResponse,
  OrganizationInvite,
  OrganizationMember,
  OrganizationSummary,
  SavingsReport,
  StatusResponse,
  Tenant,
  Token,
  User,
  PlatformSettings,
} from '~/types/accelerator'

function apiErrorMessage(err: unknown): string {
  const e = err as { data?: { error?: string, statusMessage?: string }, statusMessage?: string, message?: string }
  return e.data?.error || e.data?.statusMessage || e.statusMessage || e.message || 'Request failed'
}

function authHeaders(): Record<string, string> {
  const { token } = useAuth()
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (token.value) {
    headers.Authorization = `Bearer ${token.value}`
  }
  return headers
}

export function useAcceleratorApi() {
  async function getPlatformSettings() {
    return $fetch<PlatformSettings>('/api/platform')
  }

  async function requestCode(email: string) {
    return $fetch<StatusResponse>('/api/auth/request-code', {
      method: 'POST',
      body: { email },
    })
  }

  async function verifyCode(email: string, code: string) {
    return $fetch<AuthVerifyResponse>('/api/auth/verify', {
      method: 'POST',
      body: { email, code },
    })
  }

  async function updateProfile(name: string) {
    return $fetch<User>('/api/me', {
      method: 'PATCH',
      headers: authHeaders(),
      body: { name },
    })
  }

  async function logout() {
    return $fetch<StatusResponse>('/api/auth/logout', {
      method: 'POST',
      headers: authHeaders(),
    })
  }

  async function me() {
    return $fetch<User>('/api/me', { headers: authHeaders() })
  }

  async function listOrganizations() {
    return $fetch<OrganizationSummary[]>('/api/me/organizations', { headers: authHeaders() })
  }

  async function createOrganization(name: string, id?: string) {
    return $fetch<OrganizationSummary>('/api/me/organizations', {
      method: 'POST',
      headers: authHeaders(),
      body: { name, id },
    })
  }

  async function listMyInvites() {
    return $fetch<OrganizationInvite[]>('/api/me/invites', { headers: authHeaders() })
  }

  async function acceptInvite(inviteId: string) {
    return $fetch<OrganizationSummary>(`/api/me/invites/${encodeURIComponent(inviteId)}/accept`, {
      method: 'POST',
      headers: authHeaders(),
    })
  }

  async function listTenants() {
    return $fetch<Tenant[]>('/api/tenants', { headers: authHeaders() })
  }

  async function getTenant(tenantId: string) {
    return $fetch<Tenant>(`/api/tenants/${encodeURIComponent(tenantId)}`, { headers: authHeaders() })
  }

  async function createTenant(id: string, name: string) {
    return $fetch<Tenant>('/api/tenants', {
      method: 'POST',
      headers: authHeaders(),
      body: { id, name },
    })
  }

  async function listMembers(tenantId: string) {
    return $fetch<OrganizationMember[]>(`/api/tenants/${encodeURIComponent(tenantId)}/members`, {
      headers: authHeaders(),
    })
  }

  async function inviteMember(tenantId: string, email: string, role?: string) {
    return $fetch<OrganizationInvite>(`/api/tenants/${encodeURIComponent(tenantId)}/invites`, {
      method: 'POST',
      headers: authHeaders(),
      body: { email, role },
    })
  }

  async function listTenantInvites(tenantId: string) {
    return $fetch<OrganizationInvite[]>(`/api/tenants/${encodeURIComponent(tenantId)}/invites`, {
      headers: authHeaders(),
    })
  }

  async function revokeInvite(tenantId: string, inviteId: string) {
    return $fetch<StatusResponse>(
      `/api/tenants/${encodeURIComponent(tenantId)}/invites/${encodeURIComponent(inviteId)}`,
      { method: 'DELETE', headers: authHeaders() },
    )
  }

  async function removeMember(tenantId: string, userId: string) {
    return $fetch<StatusResponse>(
      `/api/tenants/${encodeURIComponent(tenantId)}/members/${encodeURIComponent(userId)}`,
      { method: 'DELETE', headers: authHeaders() },
    )
  }

  async function requestDeleteOrg(tenantId: string) {
    return $fetch<StatusResponse>(`/api/tenants/${encodeURIComponent(tenantId)}/delete/request-code`, {
      method: 'POST',
      headers: authHeaders(),
    })
  }

  async function confirmDeleteOrg(tenantId: string, code: string) {
    return $fetch<StatusResponse>(`/api/tenants/${encodeURIComponent(tenantId)}/delete/confirm`, {
      method: 'POST',
      headers: authHeaders(),
      body: { code },
    })
  }

  async function listConnections(tenantId: string) {
    return $fetch<Connection[]>(`/api/tenants/${encodeURIComponent(tenantId)}/connections`, {
      headers: authHeaders(),
    })
  }

  async function createConnection(tenantId: string, name: string, uri: string) {
    return $fetch<Connection>(`/api/tenants/${encodeURIComponent(tenantId)}/connections`, {
      method: 'POST',
      headers: authHeaders(),
      body: { name, uri },
    })
  }

  async function updateConnection(tenantId: string, connectionId: string, body: { name?: string, uri?: string }) {
    return $fetch<Connection>(
      `/api/tenants/${encodeURIComponent(tenantId)}/connections/${encodeURIComponent(connectionId)}`,
      { method: 'PUT', headers: authHeaders(), body },
    )
  }

  async function deleteConnection(tenantId: string, connectionId: string) {
    return $fetch<StatusResponse>(
      `/api/tenants/${encodeURIComponent(tenantId)}/connections/${encodeURIComponent(connectionId)}`,
      { method: 'DELETE', headers: authHeaders() },
    )
  }

  async function testConnection(tenantId: string, connectionId: string) {
    return $fetch<StatusResponse>(
      `/api/tenants/${encodeURIComponent(tenantId)}/connections/${encodeURIComponent(connectionId)}/test`,
      { method: 'POST', headers: authHeaders() },
    )
  }

  async function getPolicy(tenantId: string) {
    return $fetch<CachePolicy>(`/api/tenants/${encodeURIComponent(tenantId)}/policy`, {
      headers: authHeaders(),
    })
  }

  async function setDefaultTtl(tenantId: string, defaultTtlSeconds: number) {
    return $fetch<StatusResponse>(`/api/tenants/${encodeURIComponent(tenantId)}/policy/defaults`, {
      method: 'PUT',
      headers: authHeaders(),
      body: { defaultTtlSeconds },
    })
  }

  async function setCollectionPolicy(tenantId: string, dbColl: string, policy: CollectionPolicy) {
    return $fetch<StatusResponse>(
      `/api/tenants/${encodeURIComponent(tenantId)}/policy/collections/${encodeURIComponent(dbColl)}`,
      { method: 'PUT', headers: authHeaders(), body: policy },
    )
  }

  async function listTokens(tenantId: string, connectionId: string) {
    return $fetch<Token[]>(
      `/api/tenants/${encodeURIComponent(tenantId)}/connections/${encodeURIComponent(connectionId)}/tokens`,
      { headers: authHeaders() },
    )
  }

  async function issueToken(tenantId: string, connectionId: string, description?: string) {
    return $fetch<IssueTokenResponse>(
      `/api/tenants/${encodeURIComponent(tenantId)}/connections/${encodeURIComponent(connectionId)}/tokens`,
      {
        method: 'POST',
        headers: authHeaders(),
        body: { description },
      },
    )
  }

  async function revokeToken(tokenId: string) {
    return $fetch<StatusResponse>(`/api/tokens/${encodeURIComponent(tokenId)}`, {
      method: 'DELETE',
      headers: authHeaders(),
    })
  }

  async function invalidate(tenantId: string, req: InvalidateRequest = {}) {
    return $fetch<InvalidateResponse>(`/api/tenants/${encodeURIComponent(tenantId)}/invalidate`, {
      method: 'POST',
      headers: authHeaders(),
      body: req,
    })
  }

  async function getSavings(tenantId: string) {
    return $fetch<SavingsReport>(`/api/tenants/${encodeURIComponent(tenantId)}/savings`, {
      headers: authHeaders(),
    })
  }

  async function checkHealth() {
    return $fetch<{ ok: boolean, accelerator: string, health: string | null }>('/api/health')
  }

  return {
    getPlatformSettings,
    requestCode,
    verifyCode,
    updateProfile,
    logout,
    me,
    listOrganizations,
    createOrganization,
    listMyInvites,
    acceptInvite,
    listTenants,
    getTenant,
    createTenant,
    listMembers,
    inviteMember,
    listTenantInvites,
    revokeInvite,
    removeMember,
    requestDeleteOrg,
    confirmDeleteOrg,
    listConnections,
    createConnection,
    updateConnection,
    deleteConnection,
    testConnection,
    getPolicy,
    setDefaultTtl,
    setCollectionPolicy,
    listTokens,
    issueToken,
    revokeToken,
    invalidate,
    getSavings,
    checkHealth,
    apiErrorMessage,
  }
}
