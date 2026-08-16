import { setupServer } from 'msw/node'

// MSW intercepts at the NETWORK level, so the generated client is exercised
// rather than stubbed. A hand-rolled fetch mock would prove the test's own
// double and nothing about openapi-fetch's request shaping.
export const server = setupServer()
