// The same forbidden edge as bad-widget's first import, written through the
// alias. This is the spelling a checker that only matches './' misses.
import { api } from '@/api/client'
export const AliasWidget = () => api
