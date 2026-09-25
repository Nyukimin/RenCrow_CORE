#!/usr/bin/env python3
"""Out-of-tree Step20 backlog item_id -> backlog_item_id remapper (stdlib only).

Why out-of-tree: TestStep20MigrationSourceIsRemovedAfterCutover requires the
in-tree Step20 migration source to be deleted after cutover, and
`git log --all -- cmd/rencrow-step20* internal/application/step20backlogitemmigration*`
is empty, so no in-tree remapper exists on any branch.  This script keeps the
contract outside the scanned source tree (Tmp/ is gitignored) so the
post-cutover appends can still be remapped.

Contract
  * renames the top-level item_id key to backlog_item_id, value unchanged
  * fails closed on malformed JSON, non-object lines, and lines whose two id
    keys disagree
  * preserves lines that carry neither id key (backfill receipt lines) verbatim
  * refuses --apply when the resolved destination is under /srv/rencrow
  * idempotent: a second run over applied output reports converted=0
  * models both measured read paths and refuses --apply when the effective read
    state would regress (see --reconcile)

Read paths measured on source (internal/infrastructure/backlog/jsonl_store.go
listLocked, plus the BacklogItem DTOs in internal/domain/backlog):
  head_reader      HEAD f159ed9 reads backlog_item_id only; a line without a
                   non-empty backlog_item_id is skipped; per id the LAST line
                   wins (map overwrite in file order, no timestamp reconcile).
  deployed_reader  the deployed binary (embedded vcs.revision 351def7, built
                   from main) declares json:"item_id" only, so it sees only
                   lines carrying item_id and skips every canonical line.
Because dual-read is removed (s20_step18_backlog_single_read_boundary), the
cutover must rename the remaining legacy lines in the same maintenance window
as the HEAD deploy.
"""

import argparse
import json
import os
import sys

FORBIDDEN_APPLY_ROOT = "/srv/rencrow"
CANON = "backlog_item_id"
LEGACY = "item_id"
ID_KEYS = (CANON, LEGACY)


def text(value):
    return str(value).strip() if value is not None else ""


def read_entries(path):
    entries = []
    with open(path, "r", encoding="utf-8") as handle:
        for lineno, raw in enumerate(handle, start=1):
            if not raw.strip():
                continue
            try:
                obj = json.loads(raw)
            except json.JSONDecodeError as exc:
                raise SystemExit("fail closed: %s line %d: %s" % (path, lineno, exc))
            if not isinstance(obj, dict):
                raise SystemExit("fail closed: %s line %d is not a JSON object" % (path, lineno))
            entries.append((lineno, obj))
    return entries


def strip_id_keys(obj):
    return {k: v for k, v in obj.items() if k not in ID_KEYS}


def fingerprint(obj):
    """Value identity of a revision, independent of which id key carries it."""
    return json.dumps(strip_id_keys(obj), ensure_ascii=False, sort_keys=True)


def remap_line(obj):
    canon, legacy = CANON in obj, LEGACY in obj
    if canon and legacy:
        if text(obj[CANON]) != text(obj[LEGACY]):
            raise SystemExit(
                "fail closed: line carries disagreeing ids %r %r" % (obj[LEGACY], obj[CANON])
            )
        out = dict(obj)
        out.pop(LEGACY)
        return out, "converted"
    if canon:
        return obj, "already_canonical" if text(obj[CANON]) else "empty_canonical"
    if legacy:
        if not text(obj[LEGACY]):
            return obj, "empty_legacy"
        out = dict(obj)
        out[CANON] = out.pop(LEGACY)
        return out, "converted"
    return obj, "no_id_key"


def effective_state(entries, reader):
    """Simulate a read path: per id the last carrying line wins (listLocked)."""
    state = {}
    for lineno, obj in entries:
        if reader == "head_reader":
            ident = text(obj.get(CANON))
        elif reader == "deployed_reader":
            ident = text(obj.get(LEGACY)) if LEGACY in obj else ""
        elif reader == "either_key":
            ident = text(obj.get(CANON)) or text(obj.get(LEGACY))
        else:
            raise SystemExit("unknown reader %s" % reader)
        if not ident:
            continue
        state[ident] = (lineno, obj)
    return state


def intent_state(entries):
    """Newest revision per id across both keys (ties resolved by later line)."""
    best = {}
    for lineno, obj in entries:
        ident = text(obj.get(CANON)) or text(obj.get(LEGACY))
        if not ident:
            continue
        rank = (text(obj.get("updated_at")), lineno)
        if ident not in best or rank >= best[ident][0]:
            best[ident] = (rank, obj)
    return {k: (v[0][1], v[1]) for k, v in best.items()}


def diff_states(left, right, left_label, right_label):
    out = []
    only_left = sorted(set(left) - set(right))
    only_right = sorted(set(right) - set(left))
    if only_left:
        out.append("ids_only_in_%s(%d):%s" % (left_label, len(only_left), only_left[:5]))
    if only_right:
        out.append("ids_only_in_%s(%d):%s" % (right_label, len(only_right), only_right[:5]))
    for ident in sorted(set(left) & set(right)):
        if fingerprint(left[ident][1]) != fingerprint(right[ident][1]):
            out.append(
                "revision_changed:%s(%s_line=%d %s_line=%d)"
                % (ident, left_label, left[ident][0], right_label, right[ident][0])
            )
    return out


def run_cli(*cli_args):
    import subprocess

    return subprocess.run(
        [sys.executable, os.path.realpath(__file__)] + list(cli_args),
        capture_output=True,
        text=True,
    )


def self_test():
    """Offline contract fixtures for the s20_backlog_item_id_offline_remapper
    tool contract: rename fidelity, fail-closed parsing, /srv/rencrow apply
    refusal, idempotence and the reconcile guard.  Needs no network, no live
    backlog and no running CORE, so it runs in the pre-commit sandbox."""
    import tempfile

    checks = []

    def check(name, ok, detail=""):
        checks.append((name, bool(ok)))
        print("selftest %-46s %s%s" % (name, "PASS" if ok else "FAIL", "" if ok else " :: " + detail))

    def rows(path):
        return [json.loads(line) for line in open(path, encoding="utf-8") if line.strip()]

    with tempfile.TemporaryDirectory(prefix="s20remap-selftest-") as work:
        src = os.path.join(work, "plain.jsonl")
        with open(src, "w", encoding="utf-8") as handle:
            handle.write('{"item_id":"atlas:a","title":"A","updated_at":"2026-08-21T22:26:00+09:00"}\n')
            handle.write('{"record_type":"atlas_backfill_import","import_id":"imp-1"}\n')
        out = os.path.join(work, "plain.out.jsonl")

        first = run_cli("--input", src, "--output", out, "--apply")
        got = rows(out) if first.returncode == 0 else []
        check("rename_keeps_value_drops_legacy_key",
              first.returncode == 0 and got
              and got[0].get("backlog_item_id") == "atlas:a"
              and "item_id" not in got[0]
              and got[0].get("title") == "A"
              and got[0].get("updated_at") == "2026-08-21T22:26:00+09:00",
              "rc=%s err=%s" % (first.returncode, first.stderr[-200:]))
        check("neither_key_line_preserved",
              len(got) == 2 and got[1].get("record_type") == "atlas_backfill_import"
              and got[1].get("import_id") == "imp-1"
              and "backlog_item_id" not in got[1], "rows=%s" % got[1:])

        dry = run_cli("--input", src)
        check("dry_run_reports_ready_without_writing",
              dry.returncode == 0 and '"status": "ready"' in dry.stdout
              and '"mode": "dry-run"' in dry.stdout, dry.stdout[-200:])

        rerun = run_cli("--input", out)
        check("idempotent_rerun_reports_noop",
              rerun.returncode == 0 and '"status": "noop"' in rerun.stdout
              and '"converted"' not in rerun.stdout, rerun.stdout[-260:])
        second = run_cli("--input", out, "--output", out + ".2", "--apply")
        check("second_apply_is_byte_identical",
              second.returncode == 0
              and open(out, "rb").read() == open(out + ".2", "rb").read(),
              second.stderr[-200:])

        bad = os.path.join(work, "bad.jsonl")
        with open(bad, "w", encoding="utf-8") as handle:
            handle.write('{"item_id":"x",}\n')
        malformed = run_cli("--input", bad)
        check("fail_closed_malformed_json",
              malformed.returncode != 0 and "fail closed" in malformed.stderr,
              "rc=%s out=%s err=%s" % (malformed.returncode, malformed.stdout[-120:], malformed.stderr[-160:]))
        with open(bad, "w", encoding="utf-8") as handle:
            handle.write("[1, 2, 3]\n")
        check("fail_closed_non_object_line", run_cli("--input", bad).returncode != 0, "")
        with open(bad, "w", encoding="utf-8") as handle:
            handle.write('{"item_id":"x","backlog_item_id":"y"}\n')
        disagree_out = os.path.join(work, "disagree.out.jsonl")
        disagree = run_cli("--input", bad, "--output", disagree_out, "--apply")
        check("fail_closed_disagreeing_id_keys",
              disagree.returncode != 0 and "disagreeing ids" in disagree.stderr,
              "rc=%s err=%s" % (disagree.returncode, disagree.stderr[-200:]))
        check("fail_closed_writes_no_output", not os.path.exists(disagree_out), disagree_out)

        srv = "/srv/rencrow/db/core/databases/ops/selftest-backlog.jsonl"
        refusal = run_cli("--input", src, "--output", srv, "--apply")
        check("refuses_apply_under_srv_rencrow",
              refusal.returncode != 0 and "refused apply under /srv/rencrow" in refusal.stderr,
              "rc=%s err=%s" % (refusal.returncode, refusal.stderr[-220:]))
        check("srv_refusal_created_no_file", not os.path.exists(srv), srv)

        stale = os.path.join(work, "stale.jsonl")
        with open(stale, "w", encoding="utf-8") as handle:
            handle.write('{"backlog_item_id":"atlas:newer","status":"ok","updated_at":"2026-08-23T00:30:10Z"}\n')
            handle.write('{"item_id":"atlas:newer","status":"proposal_review","updated_at":"2026-08-21T22:26:00+09:00"}\n')
        strict_out = os.path.join(work, "stale.strict.jsonl")
        strict = run_cli("--input", stale, "--output", strict_out, "--apply")
        check("strict_refuses_older_winning_revision",
              strict.returncode != 0 and "effective read state would regress" in strict.stderr
              and not os.path.exists(strict_out),
              "rc=%s err=%s" % (strict.returncode, strict.stderr[-260:]))
        rec_out = os.path.join(work, "stale.reconciled.jsonl")
        rec = run_cli("--input", stale, "--output", rec_out, "--apply",
                      "--reconcile", "append-current")
        rec_rows = rows(rec_out) if rec.returncode == 0 else []
        check("append_current_keeps_newest_winner",
              rec.returncode == 0 and rec_rows
              and rec_rows[-1].get("backlog_item_id") == "atlas:newer"
              and rec_rows[-1].get("status") == "ok"
              and '"effective_read_state_regressions": 0' in rec.stdout,
              "rc=%s rows=%s err=%s" % (rec.returncode, rec_rows[-1:], rec.stderr[-200:]))
        check("append_current_keeps_history_lines", len(rec_rows) == 3, "rows=%d" % len(rec_rows))
        check("append_current_output_is_noop_on_rerun",
              '"status": "noop"' in run_cli("--input", rec_out).stdout, "")

        mixed = os.path.join(work, "mixed.jsonl")
        with open(mixed, "w", encoding="utf-8") as handle:
            handle.write('{"backlog_item_id":"c1","v":1}\n')
            handle.write('{"item_id":"l1","v":2}\n')
        entries = read_entries(mixed)
        heads = effective_state(entries, "head_reader")
        deployed = effective_state(entries, "deployed_reader")
        check("reader_models_match_measured_source",
              set(heads) == {"c1"} and set(deployed) == {"l1"},
              "head=%s deployed=%s" % (sorted(heads), sorted(deployed)))

        failed = [name for name, ok in checks if not ok]
        print("selftest summary: %d checks, %d failures" % (len(checks), len(failed)))
        if failed:
            print("selftest failed: %s" % ", ".join(failed))
            return 1
        print("SELFTEST_OK")
        return 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", help="JSONL to remap; required unless --self-test")
    parser.add_argument("--output")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument(
        "--reconcile",
        choices=["strict", "append-current"],
        default="strict",
        help="strict refuses any effective-state regression; append-current "
             "re-appends the newest revision of an id whose trailing legacy line "
             "is an older revision, so the rename cannot regress the read state",
    )
    parser.add_argument(
        "--self-test",
        action="store_true",
        help="run the offline contract fixtures in a temp dir and exit non-zero "
             "on the first failure; needs no network, no live backlog and no CORE",
    )
    args = parser.parse_args()

    if args.self_test:
        return self_test()

    if not args.input:
        parser.error("--input is required unless --self-test is given")

    src = os.path.realpath(args.input)
    dest = os.path.realpath(args.output) if args.output else src
    entries = read_entries(src)

    stats = {}
    mapped = []
    for _lineno, obj in entries:
        new_obj, state = remap_line(obj)
        stats[state] = stats.get(state, 0) + 1
        mapped.append(new_obj)

    after_entries = list(zip([ln for ln, _ in entries], mapped))
    baseline = intent_state(entries)
    head_before = effective_state(entries, "head_reader")
    deployed_before = effective_state(entries, "deployed_reader")
    after = effective_state(after_entries, "head_reader")

    changed = diff_states(baseline, after, "intent_baseline", "after_remap")
    appended = []
    if changed and args.reconcile == "append-current":
        for entry in changed:
            if not entry.startswith("revision_changed:"):
                raise SystemExit("refused: append-current cannot reconcile %s" % entry)
            ident = entry.split("(", 1)[0].split(":", 1)[1]
            winner = dict(baseline[ident][1])
            winner.pop(LEGACY, None)
            winner[CANON] = ident
            mapped.append(winner)
            after_entries.append((len(mapped), winner))
            appended.append(
                {
                    "backlog_item_id": ident,
                    "appended_position": len(mapped),
                    "appended_updated_at": text(winner.get("updated_at")),
                    "superseded_by_identity": CANON,
                }
            )
        after = effective_state(after_entries, "head_reader")
        changed = diff_states(baseline, after, "intent_baseline", "after_remap")
        if changed:
            raise SystemExit(
                "refused: effective state still regresses after reconcile: %s" % changed[:5]
            )

    report = {
        "schema_version": "rencrow-step20-backlog-item-remap/v2",
        "mode": "apply" if args.apply else "dry-run",
        "reconcile_mode": args.reconcile,
        "input": src,
        "output": dest,
        "lines": len(entries),
        "stats": stats,
        "ids_intent_baseline": len(baseline),
        "ids_head_reader_before": len(head_before),
        "ids_deployed_reader_before": len(deployed_before),
        "ids_after_remap": len(after),
        "effective_read_state_regressions": len(changed),
        "effective_read_state_regression_sample": changed[:10],
        "reconciliation_appended": appended,
        "legacy_ids_also_canonical": len(set(deployed_before) & set(head_before)),
        "legacy_ids_without_canonical_line": sorted(set(deployed_before) - set(head_before))[:10],
        "canonical_ids_invisible_to_deployed_reader": len(set(head_before) - set(deployed_before)),
    }

    if args.apply:
        if dest == FORBIDDEN_APPLY_ROOT or dest.startswith(FORBIDDEN_APPLY_ROOT + os.sep):
            raise SystemExit("refused apply under %s: %s" % (FORBIDDEN_APPLY_ROOT, dest))
        if changed:
            raise SystemExit(
                "refused apply (--reconcile %s): effective read state would regress "
                "for %d id(s): %s" % (args.reconcile, len(changed), changed[:5])
            )
        tmp = dest + ".remap.tmp"
        with open(tmp, "w", encoding="utf-8") as handle:
            for obj in mapped:
                handle.write(json.dumps(obj, ensure_ascii=False, separators=(",", ":")) + "\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(tmp, dest)
        report["status"] = "applied"
    else:
        report["status"] = "noop" if stats.get("converted", 0) == 0 else "ready"

    json.dump(report, sys.stdout, ensure_ascii=False, indent=2, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    raise SystemExit(main())
