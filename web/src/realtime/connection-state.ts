/**
 * What the transport says about itself, and the reason it is declared HERE.
 *
 * The plan created this file in Task 8, alongside the indicator that renders
 * it, and had Task 10's transport import it from `shell/`. That points the
 * dependency backwards: the transport PRODUCES this state and the shell merely
 * DISPLAYS it, so `realtime/` owning the type is what lets the shell be
 * deleted, replaced or tested without the transport noticing. The arch check's
 * own comment (tools/arch/import-graph.ts) already names that edge as the one
 * it eliminates rather than bans; declaring the type anywhere else would put
 * the violation back in the tree it was written to keep out.
 */
export type ConnectionState = 'live' | 'reconnecting' | 'stopped'
