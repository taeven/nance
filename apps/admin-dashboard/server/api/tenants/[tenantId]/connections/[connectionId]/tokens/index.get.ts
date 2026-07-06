import type { Token } from '~/types/accelerator'

export default defineEventHandler(async (event) => {
  const tenantId = getRouterParam(event, 'tenantId')
  const connectionId = getRouterParam(event, 'connectionId')
  if (!tenantId || !connectionId) {
    throw createError({ statusCode: 400, statusMessage: 'tenantId and connectionId required' })
  }
  return acceleratorFetch<Token[]>(
    event,
    `/api/v1/tenants/${encodeURIComponent(tenantId)}/connections/${encodeURIComponent(connectionId)}/tokens`,
  )
})
