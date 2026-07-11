import type { TenantMetrics } from '~/types/accelerator'

export default defineEventHandler(async (event) => {
  const tenantId = getRouterParam(event, 'tenantId')
  if (!tenantId) {
    throw createError({ statusCode: 400, statusMessage: 'tenantId required' })
  }
  const query = getQuery(event)
  const window = typeof query.window === 'string' ? query.window : '1h'
  return acceleratorFetch<TenantMetrics>(
    event,
    `/api/v1/tenants/${encodeURIComponent(tenantId)}/metrics`,
    { query: { window } },
  )
})
