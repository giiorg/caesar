// A FOURTH consumer, and it reaches helper.ts ONLY through a dynamic import().
// The Global Constraint says a consumer is a direct importer "static or
// dynamic"; without this file the `import\s*\(` half of IMPORT_RE is dead
// weight — MEASURED: deleting it left all 13 tests green. A second consumer
// hiding behind a lazy route import is precisely the case the rule exists for.
export const d = async () => (await import('../a/helper')).shared
