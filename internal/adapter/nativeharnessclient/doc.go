// Package nativeharnessclient holds the CORE side of the delegation to
// RenCrow_Harness (adopted design, WP07 and WP08).
//
// This package contains the pure components: the typed ContextBlock revision
// builder (F32) with its block count limit, the OriginProof issuer (F33) with
// its key file reader, and the check of the Harness config caller section that
// the CORE startup validation (internal/adapter/config, native_harness
// section) uses. It starts no process and does not import the Harness module.
//
// The process start, the stdio client, the admission of the execution profile
// shiro_native_coding_v1 and the delegation itself are in the child package
// delegation, the only package that imports the Harness module (an
// architecture test fixes this). Nothing here signs a request yet: the
// OriginProof issuer is not wired into the delegation (design inquiries D5 and
// D6), so every delegation is sent as Automation.
//
// All of it is separate from the existing ToolHarness and super_agent_harness.
// The formulas are owned by the CORE docs (04 ContextBlock section, 07
// delegation safety section, 05 delegation settings section); this package
// never imports RenCrow_Harness code.
package nativeharnessclient
