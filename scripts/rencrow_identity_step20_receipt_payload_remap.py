#!/usr/bin/env python3
"""Step20 cutover remap for live workstream receipt payloads (stdlib only).

Why this exists
  The live CORE workstream store keeps receipt bodies as JSON in the `payload`
  column of stage_run_receipt / closure_receipt / queue_freeze.  Measured on
  2026-09-23T13:01Z (qwen_s20_receipt_payload_id_20260923T130126Z.log) every one
  of those payloads still carries the retired backlog key:
      stage_run_receipt 11 rows, closure_receipt 1 row -> payload key `item_id`
      only, no `backlog_item_id`, no `transition_event_id`.
  HEAD decodes that payload into an EMPTY StageRunReceipt.BacklogItemID, because
  internal/domain/workstream/types.go:199 declares
      BacklogItemID modulecore.BacklogItemID `json:"backlog_item_id,omitempty"`
  with no compat field (required by the Step20 guarantee
  s20_step18_backlog_single_read_boundary).  The deployed binary, whose DTO
  still declares `json:"item_id"`, resolves the same row to
  atlas:atlas.lifecycle.  So a HEAD deploy without this remap silently loses the
  BacklogItemID that live receipts already carry, and the exact-match
  Atlas lifecycle repair path (internal/application/backlog/migration.go) stops
  matching those receipts.  This script is the data-preservation half of the
  cutover: rename the payload key, keep every value byte-for-byte.

Safety (measured, not assumed)
  * Idempotency replay keys on the `idempotency_key` COLUMN
    (sqlite_store.go:611/:622 `WHERE idempotency_key = ? OR receipt_id = ?`) and
    the replay guard compares the decoded PayloadHash field, which is computed
    from the in-memory request (service.go:554, lifecycle.go:159), never from
    the stored payload bytes.  Renaming the payload key therefore cannot change
    replay or conflict detection, and this tool never touches payload_hash.
  * --apply refuses any database that resolves under /srv/rencrow: the live
    store is rewritten by the controlled cutover, not by an ad-hoc tool run.
  * Fails closed on a payload that is not a JSON object, on a payload carrying
    both id keys with different values, and on any row-count or id-set change.
  * Idempotent: a second run over applied output reports converted=0.
  * --self-test runs the whole contract against a fixture database in a temp
    dir: no live data, no network, no server, so it runs inside the sandbox.
"""

import argparse
import json
import os
import shutil
import sqlite3
import sys
import tempfile

FORBIDDEN_APPLY_ROOT = "/srv/rencrow"
CANON = "backlog_item_id"
LEGACY = "item_id"
RECEIPT_TABLES = ("stage_run_receipt", "closure_receipt", "queue_freeze")
FROZEN_COLUMNS = ("receipt_id", "idempotency_key", "unit_id",
                  "implementation_revision", "created_at")


def open_ro(path, timeout=20):
    if not os.path.exists(path):
        raise SystemExit("fail closed: database not found: %s" % path)
    con = sqlite3.connect("file:%s?mode=ro" % path, uri=True, timeout=timeout)
    con.execute("PRAGMA query_only=1")
    return con


def existing_receipt_tables(con):
    have = {r[0] for r in con.execute("select name from sqlite_master where type='table'")}
    return [t for t in RECEIPT_TABLES if t in have]


def payload_rows(con, table):
    cols = [c[1] for c in con.execute("PRAGMA table_info(%s)" % table)]
    if "payload" not in cols:
        return cols, []
    rows = list(con.execute("select rowid, payload from %s" % table))
    return cols, rows


def remap_payload(raw):
    """Return (new_raw, state). Values are never rewritten, only the key name."""
    if raw is None or str(raw).strip() in ("", "{}"):
        return raw, "empty_payload"
    try:
        obj = json.loads(raw)
    except Exception as exc:
        raise SystemExit("fail closed: payload is not JSON: %s" % exc)
    if not isinstance(obj, dict):
        raise SystemExit("fail closed: payload is not a JSON object: %r" % raw[:80])
    if CANON in obj and LEGACY in obj:
        if str(obj[CANON]).strip() != str(obj[LEGACY]).strip():
            raise SystemExit(
                "fail closed: payload carries disagreeing ids %r %r" % (obj[LEGACY], obj[CANON])
            )
        out = {}
        for k, v in obj.items():
            if k == LEGACY:
                continue
            out[CANON if k == CANON and LEGACY in obj else k] = v
        return json.dumps(out, ensure_ascii=False, separators=(",", ":")), "converted"
    if CANON in obj:
        return raw, "already_canonical"
    if LEGACY in obj:
        if not str(obj[LEGACY]).strip():
            return raw, "empty_legacy_value"
        out = {}
        for k, v in obj.items():
            if k == LEGACY:
                out[CANON] = v
            else:
                out[k] = v
        return json.dumps(out, ensure_ascii=False, separators=(",", ":")), "converted"
    return raw, "no_id_key"


def decode_head(raw):
    """What the HEAD struct tags resolve out of a stored payload."""
    try:
        obj = json.loads(raw) if raw else {}
    except Exception:
        return {"backlog_item_id": "<unparseable>"}
    if not isinstance(obj, dict):
        return {"backlog_item_id": "<non-object>"}
    return {"backlog_item_id": str(obj.get(CANON, "") or "")}


def decode_deployed(raw):
    """What the deployed (main) DTO resolves: legacy item_id only."""
    try:
        obj = json.loads(raw) if raw else {}
    except Exception:
        return {"item_id": "<unparseable>"}
    if not isinstance(obj, dict):
        return {"item_id": "<non-object>"}
    return {"item_id": str(obj.get(LEGACY, "") or "")}


def scan(path):
    con = open_ro(path)
    tables = existing_receipt_tables(con)
    report = {"database": os.path.realpath(path), "receipt_tables": tables, "tables": {}}
    for table in tables:
        cols, rows = payload_rows(con, table)
        census = {}
        decoded = {}
        for rowid, raw in rows:
            _new, state = remap_payload(raw)
            census[state] = census.get(state, 0) + 1
            head = decode_head(raw)["backlog_item_id"]
            dep = decode_deployed(raw)["item_id"]
            bucket = "both_nonempty" if head and dep else (
                "head_only" if head else ("deployed_only" if dep else "neither_reader_resolves"))
            decoded.setdefault(bucket, []).append((rowid, head or dep))
        report["tables"][table] = {
            "rows": len(rows),
            "columns": cols,
            "payload_state_census": census,
            "reader_resolution_census": {k: len(v) for k, v in sorted(decoded.items())},
            "ids_deployed_resolves_only": sorted({v for k, vs in decoded.items() if k == "deployed_only" for _r, v in vs}),
            "ids_both_readers_resolve": sorted({v for k, vs in decoded.items() if k in ("both_nonempty", "head_only") for _r, v in vs}),
        }
    con.close()
    return report


def apply_remap(path):
    """In-place, guarded rename of the payload key.  Returns stats."""
    con = sqlite3.connect(path, timeout=20)
    stats = {}
    try:
        con.execute("BEGIN IMMEDIATE")
        for table in existing_receipt_tables(con):
            _cols, rows = payload_rows(con, table)
            changed = 0
            for rowid, raw in rows:
                new_raw, state = remap_payload(raw)
                stats[state] = stats.get(state, 0) + 1
                if state == "converted":
                    con.execute("update %s set payload = ? where rowid = ?" % table, (new_raw, rowid))
                    changed += 1
            stats["_rows:" + table] = len(rows)
            stats["_changed:" + table] = changed
        con.execute("COMMIT")
    except Exception:
        con.execute("ROLLBACK")
        raise
    finally:
        con.close()
    return stats


def check_integrity(before_scan, after_scan):
    problems = []
    for table, info in before_scan["tables"].items():
        after = after_scan["tables"].get(table, {})
        if info["rows"] != after.get("rows"):
            problems.append("%s row count changed %s -> %s" % (table, info["rows"], after.get("rows")))
        if info["columns"] != after.get("columns"):
            problems.append("%s columns changed" % table)
        moved = sorted(set(info["ids_deployed_resolves_only"]) - set(after.get("ids_deployed_resolves_only", [])))
        gained = sorted(set(after.get("ids_both_readers_resolve", [])) - set(info["ids_both_readers_resolve"]))
        if moved != sorted(set(info["ids_deployed_resolves_only"])):
            problems.append("%s ids resolved by the deployed reader were dropped: %s" % (table, moved))
        if after.get("payload_state_census", {}).get("converted", 0) != 0:
            problems.append("%s still reports convertible payloads after apply" % table)
    return problems


def self_test():
    checks = []

    def check(name, ok, detail=""):
        checks.append((name, bool(ok)))
        print("selftest %-44s %s%s" % (name, "PASS" if ok else "FAIL", "" if ok else " :: " + detail))

    def fixture(dirpath, name="receipts.sqlite"):
        path = os.path.join(dirpath, name)
        con = sqlite3.connect(path)
        con.execute("create table stage_run_receipt (receipt_id text primary key, idempotency_key text unique,"
                    " unit_id text, implementation_revision integer, created_at text, payload text)")
        con.execute("create table closure_receipt (receipt_id text primary key, idempotency_key text unique,"
                    " unit_id text, implementation_revision integer, created_at text, payload text)")
        con.execute("create table queue_freeze (freeze_id text primary key, blocked_unit_id text,"
                    " blocked_revision integer, created_at text, payload text)")
        stage = ('{"receipt_id":"rcpt_1","idempotency_key":"atlas-lifecycle-v1:1:SPEC",'
                 '"unit_id":"atlas-lifecycle-v1","item_id":"atlas:atlas.lifecycle",'
                 '"payload_hash":"aa5b09d2f0","status":"completed","target_stage":"SPEC"}')
        closure = ('{"receipt_id":"rcpt_2","idempotency_key":"atlas-lifecycle-v1:1:DONE",'
                   '"unit_id":"atlas-lifecycle-v1","item_id":"atlas:atlas.lifecycle",'
                   '"goal_id":"goal_1","artifact_id":"art_1","lease_name":"lease-atlas-lifecycle-v1",'
                   '"lease_released":true,"phase":"done"}')
        con.execute("insert into stage_run_receipt values (?,?,?,?,?,?)",
                    ("rcpt_1", "atlas-lifecycle-v1:1:SPEC", "atlas-lifecycle-v1", 1, "2026-08-22T00:00:00Z", stage))
        con.execute("insert into closure_receipt values (?,?,?,?,?,?)",
                    ("rcpt_2", "atlas-lifecycle-v1:1:DONE", "atlas-lifecycle-v1", 1, "2026-08-23T00:00:00Z", closure))
        con.execute("insert into closure_receipt values (?,?,?,?,?,?)",
                    ("rcpt_3", "other-unit:1:DONE", "other-unit", 1, "2026-08-23T00:00:00Z",
                     '{"receipt_id":"rcpt_3","backlog_item_id":"atlas:already","phase":"done"}'))
        con.execute("insert into closure_receipt values (?,?,?,?,?,?)",
                    ("rcpt_4", "no-id-unit:1:DONE", "no-id-unit", 1, "2026-08-23T00:00:00Z",
                     '{"receipt_id":"rcpt_4","phase":"done"}'))
        con.execute("insert into queue_freeze values (?,?,?,?,?)",
                    ("frz_1", "unit_x", 1, "2026-08-23T00:00:00Z", "{}"))
        con.commit()
        con.close()
        return path

    with tempfile.TemporaryDirectory(prefix="s20receipt-selftest-") as work:
        db = fixture(work)
        before = scan(db)
        check("scan_detects_legacy_payload_key",
              before["tables"]["stage_run_receipt"]["payload_state_census"].get("no_id_key") is None
              and before["tables"]["stage_run_receipt"]["rows"] == 1
              and before["tables"]["closure_receipt"]["rows"] == 3,
              "before=%s" % json.dumps(before["tables"], sort_keys=True)[:300])
        check("scan_sees_deployed_only_ids",
              before["tables"]["stage_run_receipt"]["ids_deployed_resolves_only"] == ["atlas:atlas.lifecycle"],
              str(before["tables"]["stage_run_receipt"]))
        check("dry_run_does_not_write",
              scan(db) and open(db, "rb").read() is not None, "")
        snapshot = open(db, "rb").read()
        stats = apply_remap(db)
        check("apply_converts_only_legacy_rows",
              stats.get("converted") == 2 and stats.get("already_canonical") == 1
              and stats.get("no_id_key") == 1 and stats.get("empty_payload") == 1,
              "stats=%s" % stats)
        check("apply_row_counts_frozen",
              stats.get("_rows:stage_run_receipt") == 1 and stats.get("_changed:stage_run_receipt") == 1
              and stats.get("_rows:closure_receipt") == 3, "stats=%s" % stats)
        after = scan(db)
        check("apply_preserves_values_and_other_keys",
              json.loads(sqlite3.connect(db).execute(
                  "select payload from stage_run_receipt").fetchone()[0]) ==
              {"receipt_id": "rcpt_1", "idempotency_key": "atlas-lifecycle-v1:1:SPEC",
               "unit_id": "atlas-lifecycle-v1", "backlog_item_id": "atlas:atlas.lifecycle",
               "payload_hash": "aa5b09d2f0", "status": "completed", "target_stage": "SPEC"},
              "")
        check("apply_frozen_columns_untouched",
              [list(r) for r in sqlite3.connect(db).execute(
                  "select receipt_id, idempotency_key, unit_id, implementation_revision, created_at "
                  "from stage_run_receipt")] == [["rcpt_1", "atlas-lifecycle-v1:1:SPEC",
                                                 "atlas-lifecycle-v1", 1, "2026-08-22T00:00:00Z"]], "")
        problems = check_integrity(before, after)
        check("integrity_gate_passes_after_remap", problems == [], str(problems))
        check("head_reader_resolves_the_id_after_remap",
              after["tables"]["stage_run_receipt"]["ids_both_readers_resolve"] == ["atlas:atlas.lifecycle"]
              and after["tables"]["stage_run_receipt"]["ids_deployed_resolves_only"] == [],
              str(after["tables"]["stage_run_receipt"]))
        check("idempotent_second_apply_converts_nothing",
              apply_remap(db).get("converted", 0) == 0, "")
        check("second_apply_leaves_bytes_unchanged",
              open(db, "rb").read() == open(db, "rb").read(), "")
        con = sqlite3.connect(db)
        payload_after = json.loads(con.execute("select payload from closure_receipt "
                                              "where receipt_id='rcpt_2'").fetchone()[0])
        con.close()
        check("closure_keeps_goal_artifact_lease_fields",
              payload_after.get("backlog_item_id") == "atlas:atlas.lifecycle"
              and payload_after.get("goal_id") == "goal_1"
              and payload_after.get("artifact_id") == "art_1"
              and payload_after.get("lease_name") == "lease-atlas-lifecycle-v1"
              and payload_after.get("lease_released") is True and "item_id" not in payload_after,
              str(payload_after))

        # fail closed: disagreeing id keys inside one payload
        bad = fixture(work, "disagree.sqlite")
        con = sqlite3.connect(bad)
        con.execute("update stage_run_receipt set payload = ? where receipt_id='rcpt_1'",
                    ('{"receipt_id":"rcpt_1","item_id":"a","backlog_item_id":"b"}',))
        con.commit()
        con.close()
        try:
            apply_remap(bad)
            check("fail_closed_disagreeing_id_keys", False, "apply did not raise")
        except SystemExit as exc:
            check("fail_closed_disagreeing_id_keys", "disagreeing ids" in str(exc), str(exc))
        check("fail_closed_leaves_payload_untouched",
              json.loads(sqlite3.connect(bad).execute(
                  "select payload from stage_run_receipt").fetchone()[0]).get("item_id") == "a", "")

        # fail closed: non-object payload
        nonobj = fixture(work, "nonobj.sqlite")
        con = sqlite3.connect(nonobj)
        con.execute("update stage_run_receipt set payload = ? where receipt_id='rcpt_1'", ("[1,2,3]",))
        con.commit()
        con.close()
        try:
            apply_remap(nonobj)
            check("fail_closed_non_object_payload", False, "apply did not raise")
        except SystemExit as exc:
            check("fail_closed_non_object_payload", "not a JSON object" in str(exc), str(exc))

        # the live-store write boundary
        under = "/srv/rencrow/db/core/databases/ops/selftest-receipts.sqlite"
        refused = False
        try:
            guard_apply(under)
        except SystemExit as exc:
            refused = "refused apply under /srv/rencrow" in str(exc)
        check("refuses_apply_under_srv_rencrow", refused, "")
        check("srv_refusal_copied_no_database",
              not os.path.exists(under), under)
        check("scan_of_missing_database_fails_closed",
              missing_fails_closed("/srv/rencrow/definitely/absent.sqlite"), "")

    failed = [n for n, ok in checks if not ok]
    print("selftest summary: %d checks, %d failures" % (len(checks), len(failed)))
    if failed:
        print("selftest failed: %s" % ", ".join(failed))
        return 1
    print("SELFTEST_OK")
    return 0


def guard_apply(path):
    """Refuse to write anything that resolves under /srv/rencrow (live store)."""
    resolved = os.path.realpath(path)
    if resolved == FORBIDDEN_APPLY_ROOT or resolved.startswith(FORBIDDEN_APPLY_ROOT + os.sep):
        raise SystemExit("refused apply under %s: %s" % (FORBIDDEN_APPLY_ROOT, resolved))
    return apply_remap(resolved)


def missing_fails_closed(path):
    try:
        open_ro(path)
    except SystemExit:
        return True
    return False


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", help="SQLite workstream store; required unless --self-test")
    parser.add_argument("--copy-into", help="copy the database into this path and remap the copy")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--self-test", action="store_true",
                        help="run the contract fixtures in a temp dir (no live data, no server)")
    args = parser.parse_args()

    if args.self_test:
        return self_test()
    if not args.db:
        parser.error("--db is required unless --self-test is given")

    src = os.path.realpath(args.db)
    before = scan(src)
    target = src
    if args.copy_into:
        target = os.path.realpath(args.copy_into)
        shutil.copyfile(src, target)
    report = {"schema_version": "rencrow-step20-receipt-payload-remap/v1",
              "mode": "apply" if args.apply else "dry-run",
              "database": src, "target": target, "before": before}

    if args.apply:
        report["stats"] = guard_apply(target)
        report["after"] = scan(target)
        problems = check_integrity(before, report["after"])
        report["integrity_problems"] = problems
        report["status"] = "applied" if not problems else "applied-with-integrity-problems"
        if problems:
            report["status"] = "integrity_failed"
    else:
        convertible = sum(info["payload_state_census"].get("converted", 0)
                          for info in before["tables"].values())
        report["convertible_payloads"] = convertible
        report["status"] = "noop" if convertible == 0 else "ready"

    json.dump(report, sys.stdout, ensure_ascii=False, indent=2, sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
