import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'

export const meQueryOptions = queryOptions({
  queryKey: ['auth', 'me'],
  queryFn: async () => unwrap(await api.GET('/api/v1/auth/me')),
  ...AUTH_SCOPED_QUERY_OPTIONS,
  // The session's own probe: a failure here is an ANSWER ("no session"), not a
  // transport problem to retry into.
  retry: false,
})
