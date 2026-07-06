import type { Connection } from '~/types/accelerator'

export default defineEventHandler(async (event) => {
  const tenantId = getRouterParam(event, 'tenantId')
  const connectionId = getRouterParam(event, 'connectionId')
  if (!tenantId || !connectionId) {
    throw createError({ statusCode: 400, statusMessage: 'tenantId and connectionId required' })
  }
  const body = await readBody<{ name?: string, uri?: string }>(event)
  return acceleratorFetch<Connection>(
    event,
    `/api/v1/tenants/${encodeURIComponent(tenantId)}/connections/${encodeURIComponent(connectionId)}`,
    { method: 'PUT', body },
  )
})
