import { api } from '../api/client'
import { meQueryOptions } from '../auth/me-query'
import { openAttentionStream } from '../realtime/sse-connection'
import { Route } from '../routes/index'
export const BadWidget = () => [api, openAttentionStream, meQueryOptions, Route]
