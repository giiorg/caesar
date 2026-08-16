// AT its group root, and consumed from three different groups. `api/` is a
// declared permanent home (spec §2), so this must yield ZERO violations — the
// unclamped rule demanded it move to the fixture root, which is nowhere.
export const ApiError = 1
