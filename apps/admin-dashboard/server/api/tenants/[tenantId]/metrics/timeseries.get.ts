import type { MetricsTimeseries } from '~/types/accelerator'

export default defineEventHandler(async (event) => {
  const tenantId = getRouterParam(event, 'tenantId')
  if (!tenantId) {
    throw createError({ statusCode: 400, statusMessage: 'tenantId required' })
  }
  const query = getQuery(event)
  const metric = typeof query.metric === 'string' ? query.metric : ''
  if (!metric) {
    throw createError({ statusCode: 400, statusMessage: 'metric required' })
  }
  const window = typeof query.window === 'string' ? query.window : '24h'
  const step = typeof query.step === 'string' ? query.step : undefined
  const q: Record<string, string> = { metric, window }
  if (step) q.step = step
  return acceleratorFetch<MetricsTimeseries>(
    event,
    `/api/v1/tenants/${encodeURIComponent(tenantId)}/metrics/timeseries`,
    { query: q },
  )
})
