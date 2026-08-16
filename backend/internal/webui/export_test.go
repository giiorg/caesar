package webui

// HandlerFor exposes handlerFor to the external test package without widening
// the production surface. It is deliberately NOT an exported constructor on the
// package proper: the only bundle a caesar binary may serve is the one the
// embedui tag embeds, and an exported "serve this fs.FS" would be a second,
// untagged way to ship a UI — precisely the thing the two-twin split exists to
// prevent.
//
// This file carries no build tag, so the routing contract in
// ui_contract_test.go runs in the untagged lane (`task backend:test`, and
// therefore backend:ci and scripts/gate) as well as under embedui.
var HandlerFor = handlerFor
