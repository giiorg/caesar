import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type Harness = components['schemas']['ApiHarnessResponse']

/**
 * The harness registry.
 *
 * IT STARTS IN ITS OWN MODULE RATHER THAN LOCAL TO THE SECTION, which is the
 * opposite of the call `repo-form.tsx` made about the credential list — and the
 * difference is which group owns the resource. That query began inside
 * `projects/`, a group that merely needed it, so parking it local kept a
 * misplacement from being minted; there was a wrong answer to avoid. Harnesses
 * are `admin/`'s own, so the only question is which FILE in this group, and a
 * query beside `users-query.ts` and `credentials-query.ts` is where the next
 * reader looks. The workflow editor (Task 27) is the second consumer the plan
 * already names.
 *
 * AUTH_SCOPED, like every admin query: `GET /harnesses` is `adminOnly`
 * (`assets/api/api.go`), so a session demoted mid-visit must stop rendering it.
 *
 * `?? []` because `HarnessListResponse.harnesses` is required AND nullable — a
 * Go nil slice marshals to null. `test/render-app.tsx`'s `harnesses: null`
 * default is what keeps this coalesce on a live path rather than leaving it
 * unreachable from every test in the tree.
 */
export const harnessesQueryOptions = queryOptions({
  queryKey: ['harnesses'],
  queryFn: async () => (await unwrap(await api.GET('/api/v1/harnesses'))).harnesses ?? [],
  ...AUTH_SCOPED_QUERY_OPTIONS,
})

/**
 * The two harness kinds and, for each, the credential kinds it may bind.
 *
 * COPIED FROM `assets/harnesses.go`'s `harnessCredentialKinds`, which is the
 * authority: a claude-code container takes a Claude subscription token or an
 * Anthropic model key, a codex container takes a Codex auth.json or an OpenAI
 * model key, and a pairing outside the table produces a container that cannot
 * authenticate at all — so `RegisterHarness` and `UpdateHarness` both refuse it
 * with a field error on `credential_id`. The picker narrowing below is a
 * CONVENIENCE, not the rule; the server enforces it either way, and that is
 * what makes offering only the valid kinds honest rather than a client-side
 * policy invented here.
 *
 * `forge_token` is in NEITHER row, which is the fact that keeps this table from
 * collapsing into "any credential": a deployment that has registered a repo has
 * a forge token sitting in the same list, and it can never bind to a harness.
 *
 * There is no vocabulary in the generated types to read any of this off —
 * `kind` is a bare `string` on both `ApiHarnessResponse` and
 * `ApiCredentialResponse`, the same shape `role` and `level` have, because the
 * Go fields' underlying type is `string` and the emitter has no enum to emit.
 */
export const HARNESS_KINDS = ['claude-code', 'codex'] as const

export type HarnessKind = (typeof HARNESS_KINDS)[number]

export const HARNESS_CREDENTIAL_KINDS: Record<HarnessKind, readonly string[]> = {
  'claude-code': ['subscription_token', 'model_key'],
  codex: ['agent_auth_file', 'model_key'],
}

/**
 * `unknown` in, a kind or null out — the narrowing every read of a wire `kind`
 * needs, because the server may grow a third harness kind before this client
 * learns it. A null answer means "a kind this build does not know", and each
 * caller decides what to say about it rather than being handed a default that
 * silently claims the row is a claude-code.
 */
export const asHarnessKind = (value: unknown): HarnessKind | null =>
  typeof value === 'string' && value in HARNESS_CREDENTIAL_KINDS ? (value as HarnessKind) : null
