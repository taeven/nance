import type { Connection } from '~/types/accelerator'

export default defineEventHandler(async (event) => {
  const tenantId = getRouterParam(event, 'tenantId')
  if (!tenantId) {
    throw createError({ statusCode: 400, statusMessage: 'tenantId required' })
  }
  const body = await readBody<{ name: string, uri: string }>(event)
  if (!body?.name || !body?.uri) {
    throw createError({ statusCode: 400, statusMessage: 'name and uri are required' })
  }
  return acceleratorFetch<Connection>(
    event,
    `/api/v1/tenants/${encodeURIComponent(tenantId)}/connections`,
    { method: 'POST', body },
  )
})
