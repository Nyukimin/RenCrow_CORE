// Package delegation is the CORE side of handing work to RenCrow_Harness over
// the native protocol (docs 02, 03, 04, 05, 07 and 10, RenCrow_Harness
// sections; adopted design, WP08).
//
// It starts and supervises `rencrow-harness serve --stdio` through the Harness
// package pkg/client, admits the turns that the execution profile
// shiro_native_coding_v1 selects (NativeCodingAdmission of the orchestrator),
// and delegates one selected turn at a time (agent.NativeCodingDelegate): one
// delegation Action and Attempt in the parent Task Run, one Harness Session,
// one turn/start under that Attempt's idempotency key, and the RunResult kept
// as the typed projection.
//
// This is the only package of CORE that imports the Harness module. The pure
// parts of the adapter (the ContextBlock revision builder, the OriginProof
// issuer, the key reader) stay in the parent package, and nothing in the
// domain, the orchestrator or the configuration imports the Harness, so a
// change of the Harness protocol types is contained here (the second ground of
// docs 40-modularization: change isolation).
//
// What this package never does: select a backend (the orchestrator admission
// does, from configuration), fall back to another executor, run a Harness Tool
// as a CORE Action, copy a Harness Event into CORE's Canonical Event Store, or
// sign a request. The delegation is sent as Automation: CORE has no ThreadID at
// reception time and no store of the accepted original input yet (design
// inquiries D5 and D6), so no OriginProof can be issued, and a Human relay is
// not claimed.
package delegation
