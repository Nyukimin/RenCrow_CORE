# Presence Feature

## Owner

PORTAL surface lifecycle (shared layer between Chat and IdleChat)

## Inputs

Viewer `POST /viewer/surface-presence` payload: `viewer_client_id`, `surface`, `action`

## Outputs

Surface lease snapshot, aggregated effective-mode decision for Chat / IdleChat

## Side Effects

Lease aggregation, periodic lease expiry, optional IdleChat runtime transition when
IdleChat runtime is attached

## Persistence

In-memory lease table; no persistent store

## Logs

`viewer_client_id`, `surface`, `action`, effective_mode, lease expiry

## Error Contract

Unknown action, invalid `viewer_client_id`, and profile/surface mismatch must not mutate
state. CORE unavailable or runtime unattached must not be masked as success.

## Current Main Files

cmd/rencrow/surface_presence.go, cmd/rencrow/runtime_idlechat_handlers.go

## Ownership Boundary

This feature owns `/viewer/surface-presence` route registration. Chat and IdleChat are
sibling surfaces that each notify presence through this shared endpoint; neither
contains the other, and this feature does not depend on either being enabled. The
IdleChat runtime, when enabled, is attached to the lease controller as an optional
observer so that visibility-driven start/stop reconciliation can still run.
