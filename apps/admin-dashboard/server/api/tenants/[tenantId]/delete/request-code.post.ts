export default defineEventHandler(async (event) => {
  const tenantId = getRouterParam(event, 'tenantId')
  return acceleratorFetch(event, `/api/v1/tenants/${encodeURIComponent(tenantId || '')}/delete/request-code`, {
    method: 'POST',
  })
})
