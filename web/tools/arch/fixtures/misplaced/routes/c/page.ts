// A THIRD consumer, reached through the '@/' alias tsconfig declares. An
// alias-blind checker sees two consumers here instead of three — and in the
// case that matters, one instead of two, which reports nothing at all.
import { shared } from '@/routes/a/helper'
export const c = shared
