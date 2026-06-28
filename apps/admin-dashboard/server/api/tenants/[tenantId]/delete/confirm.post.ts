export default defineEventHandler(async (event) => {
  const tenantId = getRouterParam(event, 'tenantId')
  const body = await readBody(event)
  return acceleratorFetch(event, `/api/v1/tenants/${encodeURIComponent(tenantId || '')}/delete/confirm`, {
    method: 'POST',
    body,
  })
})
