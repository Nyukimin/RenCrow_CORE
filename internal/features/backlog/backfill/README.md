# RenCrow ATLAS Initial Backfill v1

Generated: 2026-08-21T22:26:00+09:00

## Result

- Total Atlas Items: 114
- GitHub Annotation Atlas v2 items: 68
- Original Atlas v1 carry-over items restored: 42
- Current-thread Atlas design items: 4
- Items with SpecificationRef: 20
- Captured/local Specification Artifacts: 8
- External Specification References: 3
- unresolved / needs_context: 0

## Files

- `atlas_backfill_v1.json`
  Full machine-readable Backfill Dataset.
- `atlas_items_v2.jsonl`
  One Atlas Item v2 per line. Suitable for append-only fixture/import work.
- `atlas_item_v2.schema.json`
  Minimal JSON Schema for Item v2.
- `specification_artifacts.json`
  Specification metadata and hashes.
- `specifications/`
  Captured specifications/design-decision artifacts.
- `RenCrow_CORE/internal/features/backlog/catalog/atlas_seed_v1.json`
  Proposed static Atlas seed/catalog fixture.
- `RenCrow_CORE/internal/features/backlog/testdata/atlas_backfill_v1.json`
  Proposed CORE test fixture.

## Reconstruction policy

This dataset intentionally separates:
- `direct_spec`: a specification is directly available.
- `direct_chat`: the originating design conversation is recoverable.
- `repo_spec`: current repository specification/implementation documents support the reconstruction.
- `project_summary`: only a reliable project-conversation summary is available.
- `implementation_inference`: intent is inferred from implementation evidence and must not be treated as original design wording.
- `unresolved`: insufficient source support.

No unavailable message IDs are fabricated. Logical date/topic locators are used when exact message identifiers are not available.

## Intended import behavior

This file is a seed/backfill fixture, not authority to auto-adopt or auto-deploy anything.
The future Atlas importer should validate schema, preserve provenance, deduplicate by feature_id/source hash, and write each item through the owner Atlas/Backlog workflow.

See `specifications/spec_atlas_backfill_automation_v1.md` for the future self-backfill automation contract.
