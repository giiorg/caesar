// Both consumers live in routes/deep/nested/, which is INSIDE this module's
// group. The clamp must not fire here: the required home is the deeper common
// ancestor, not the group root. A checker that always answered "the group root"
// would pass every other fixture in this directory while silently abolishing
// colocation inside routes/ — which is the rule spec §2 actually asked for.
export const util = 1
