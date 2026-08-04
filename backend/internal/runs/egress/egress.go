// Package egress is the attempt's only route off its network: a broker that
// signs and forwards the provider calls the agent is allowed to make, plus a
// CONNECT proxy for allowlisted hosts. Task 7 of Plan 06 fills the package
// in; for now it declares only the operation allowlist the RunSpec builder
// pins per harness kind.
package egress

// Op is one permitted provider operation. Anything the allowlist does not
// name is denied — the gateway is default-deny, so this table is the whole
// surface an agent can reach through its credential.
type Op struct {
	Method     string `json:"method"`
	PathPrefix string `json:"path_prefix"`
}
