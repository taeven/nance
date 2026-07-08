import type { Token } from '~/types/accelerator'

export default defineEventHandler(async (event) => {
  const tokenId = getRouterParam(event, 'tokenId')
  if (!tokenId) {
    throw createError({ statusCode: 400, statusMessage: 'tokenId required' })
  }
  return acceleratorFetch<Token>(
    event,
    `/api/v1/tokens/${encodeURIComponent(tokenId)}/reenable`,
    { method: 'POST' },
  )
})
