export interface Tenant {
  id: string
  name: string
  status: string
  created_at: string
  updated_at: string
  /** Caller's role in this org (from GET tenant) */
  role?: MemberRole
  canManage?: boolean
  canDelete?: boolean
}

export interface User {
  id: string
  email: string
  name: string
  created_at: string
  updated_at: string
}

export type MemberRole = 'owner' | 'admin' | 'member'

export interface OrganizationSummary extends Tenant {
  role: MemberRole
}

export interface OrganizationMember {
  tenantId: string
  userId: string
  email?: string
  name?: string
  role: MemberRole
  created_at: string
}

export interface OrganizationInvite {
  id: string
  tenantId: string
  tenantName?: string
  email: string
  role: MemberRole
  invitedBy?: string
  expires_at: string
  accepted_at?: string | null
  created_at: string
}

export interface AuthVerifyResponse {
  token: string
  expiresIn: number
  user: User
}

export interface CollectionPolicy {
  enabled: boolean
  ttlSeconds: number
  maxResultBytes?: number
}

export interface CachePolicy {
  tenantId: string
  defaultTtlSeconds: number
  collections: Record<string, CollectionPolicy>
  cacheKeyVersion: number
  updatedAt: string
}

export interface Token {
  id: string
  tenantId: string
  connectionId?: string
  description?: string
  created_at: string
  expires_at?: string | null
  revoked_at?: string | null
}

export interface IssueTokenResponse {
  tokenId: string
  rawToken: string
  tenantId: string
  connectionId?: string
  description?: string
  createdAt: string
  /** Full proxy URI for clients (shown only once with rawToken). */
  proxyConnectionUri?: string
}

/** Named source Mongo connection (URI never returned). */
export interface Connection {
  id: string
  tenantId: string
  name: string
  /** When true, proxy flushes cache for a collection after successful writes to it. Default false. */
  autoInvalidateOnWrite?: boolean
  lastValidatedAt?: string | null
  created_at: string
  updated_at: string
}

export interface StatusResponse {
  status: string
  [key: string]: unknown
}

export interface InvalidateRequest {
  db?: string
  coll?: string
  tags?: string[]
}

export interface InvalidateResponse {
  status: string
  tenantId: string
  db?: string
  coll?: string
  tags?: string[]
}

export interface SavingsReport {
  tenantId: string
  note?: string
  suggestedQueries?: string[]
}

export interface PlatformSettings {
  inviteOnly: boolean
  allowOrgCreation: boolean
  allowAdminBootstrap: boolean
  /** host[:port] for building client proxy connection URIs */
  proxyPublicEndpoint?: string
}
