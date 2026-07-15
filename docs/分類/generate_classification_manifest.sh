#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

out_dir="docs/分類"
all_csv="$out_dir/全資料一覧.csv"
knowledge_csv="$out_dir/Knowledge候補一覧.csv"
summary_md="$out_dir/集計.md"
baseline=$(git rev-parse origin/main)
reviewed_at="2026-07-15"

mkdir -p "$out_dir/残すもの" "$out_dir/外すもの"
: > "$all_csv"
: > "$knowledge_csv"

csv_field() {
  local value=${1-}
  value=${value//\"/\"\"}
  printf '"%s"' "$value"
}

write_row() {
  local output=$1
  shift
  local first=1
  : > /dev/null
  for value in "$@"; do
    if [[ $first -eq 0 ]]; then
      printf ',' >> "$output"
    fi
    csv_field "$value" >> "$output"
    first=0
  done
  printf '\n' >> "$output"
}

write_row "$all_csv" \
  source_path title document_class main_action knowledge_action knowledge_target \
  knowledge_type knowledge_roles provider_scope canonical_source current_status \
  superseded_by contains_local_metadata requires_manual_review reason source_state \
  content_hash bytes reviewed_at

write_row "$knowledge_csv" \
  source_path title knowledge_action knowledge_target knowledge_type knowledge_roles \
  provider_scope canonical_source current_status requires_manual_review reason \
  content_hash reviewed_at

total=0
rewrite_count=0
remove_count=0
promote_count=0
distill_count=0
history_count=0
exclude_count=0
local_count=0

while IFS= read -r -d '' path; do
  title=$(LC_ALL=C sed -n 's/^# //p' "$path" 2>/dev/null | head -n 1 || true)
  if [[ -z "$title" ]]; then
    title=$(basename "$path")
  fi

  document_class="archive_reference"
  main_action="remove"
  knowledge_action="history_only"
  knowledge_target=""
  knowledge_type=""
  knowledge_roles=""
  provider_scope="none"
  canonical_source=""
  current_status="historical"
  superseded_by=""
  manual_review="yes"
  reason="Mainの通常仕様ではなく、履歴確認用の資料"

  case "$path" in
    docs/README.md|docs/01_理解/*.md|docs/02_正本仕様/01_仕様.md|docs/02_正本仕様/01_仕様/*.md)
      document_class="public_spec_source"
      main_action="rewrite"
      knowledge_action="promote"
      knowledge_target="spec:system-and-features"
      knowledge_type="spec"
      knowledge_roles="chat,worker,coder"
      provider_scope="any"
      canonical_source="$path"
      current_status="current"
      manual_review="yes"
      reason="現行の利用者向け仕様へ再構成する主要情報源"
      ;;
    docs/02_正本仕様/03_Runtime_Config.md)
      document_class="public_spec_source"
      main_action="rewrite"
      knowledge_action="promote"
      knowledge_target="spec:runtime-config"
      knowledge_type="spec"
      knowledge_roles="chat,worker,coder"
      provider_scope="any"
      canonical_source="$path"
      current_status="current"
      manual_review="yes"
      reason="local pathとprivate topologyを除き、公開設定仕様へ再構成する"
      ;;
    docs/02_正本仕様/04_IdleChat.md)
      document_class="public_spec_source"
      main_action="rewrite"
      knowledge_action="promote"
      knowledge_target="spec:idlechat"
      knowledge_type="spec"
      knowledge_roles="chat,worker,coder"
      provider_scope="any"
      canonical_source="$path"
      current_status="current"
      manual_review="yes"
      reason="利用者向け挙動だけを公開機能仕様へ再構成する"
      ;;
    docs/02_正本仕様/09_Game_Bridge_Observer_API.md)
      document_class="public_api_source"
      main_action="rewrite"
      knowledge_action="promote"
      knowledge_target="spec:public-api"
      knowledge_type="spec"
      knowledge_roles="chat,worker,coder"
      provider_scope="any"
      canonical_source="$path"
      current_status="current"
      manual_review="yes"
      reason="絶対pathを除きPublic API契約へ統合する"
      ;;
    docs/02_正本仕様/10_RenCrow_ToBe_統合仕様.md|docs/02_正本仕様/10_RenCrow_ToBe_統合仕様/*.md)
      document_class="public_roadmap_source"
      main_action="rewrite"
      knowledge_action="distill"
      knowledge_target="spec:roadmap"
      knowledge_type="spec"
      knowledge_roles="chat,worker,coder"
      provider_scope="any"
      canonical_source="$path"
      current_status="current_target"
      manual_review="yes"
      reason="実装済みと構想を分離してPublic roadmapへ再構成する"
      ;;
    docs/02_正本仕様/02_実装仕様.md|docs/02_正本仕様/02_実装仕様/*.md|docs/02_正本仕様/05_*|docs/02_正本仕様/06_*|docs/02_正本仕様/07_*|docs/02_正本仕様/08_*|docs/02_正本仕様/11_*|docs/02_正本仕様/12_*)
      document_class="implementation_spec"
      knowledge_action="distill"
      knowledge_target="candidate:implementation-invariants"
      knowledge_type="spec"
      knowledge_roles="worker,coder"
      provider_scope="local_only"
      canonical_source="$path"
      current_status="mixed"
      reason="Mainから外し、不変条件と責務境界だけを再検証して蒸留する"
      ;;
    docs/03_記憶検索/*|docs/03_設計文書/*|docs/04_構築指標/*|docs/adr/*)
      document_class="design_and_acceptance"
      knowledge_action="distill"
      knowledge_target="candidate:design-decisions"
      knowledge_type="spec"
      knowledge_roles="worker,coder"
      provider_scope="local_only"
      canonical_source="$path"
      current_status="mixed"
      reason="設計判断、acceptance、ADRの要点だけをKnowledge候補にする"
      ;;
    docs/05_運用/*)
      document_class="operations"
      knowledge_action="distill"
      knowledge_target="candidate:operations-runbook"
      knowledge_type="runbook"
      knowledge_roles="worker,coder"
      provider_scope="local_only"
      canonical_source="$path"
      current_status="environment_sensitive"
      reason="再現可能な一般手順だけを抽出し、local情報を除去する"
      ;;
    docs/調査/20260715_173541_RenCrow_CORE_仕様再整理/01_*|docs/調査/20260715_173541_RenCrow_CORE_仕様再整理/02_*|docs/調査/20260715_173541_RenCrow_CORE_仕様再整理/03_*|docs/調査/20260715_173541_RenCrow_CORE_仕様再整理/04_*)
      document_class="verified_analysis"
      knowledge_action="distill"
      knowledge_target="candidate:verified-current-state"
      knowledge_type="spec"
      knowledge_roles="worker,coder"
      provider_scope="local_only"
      canonical_source="$path"
      current_status="verified_snapshot"
      reason="2026-07-15検証済み分類から、最新状態の要点だけを蒸留する"
      ;;
    docs/調査/*/アーキテクチャ総合.md|docs/調査/*/ユースケース逆引き.md|docs/調査/*/結合ポイントマップ.md|docs/調査/*/トレーサビリティマトリクス.md|docs/調査/*/ギャップ分析.md|docs/調査/*/リスク分析.md)
      document_class="analysis"
      knowledge_action="distill"
      knowledge_target="candidate:architecture-evidence"
      knowledge_type="module"
      knowledge_roles="worker,coder"
      provider_scope="local_only"
      canonical_source="$path"
      current_status="snapshot"
      reason="現行codeで再検証し、ownershipと結合点だけを蒸留する"
      ;;
    docs/調査/*|docs/refs/codebase-map/*|docs/refs/understand-anything/*)
      document_class="generated_analysis"
      knowledge_action="exclude"
      current_status="snapshot"
      manual_review="no"
      reason="生成解析、関数一覧、raw graphはprompt noiseが大きいため原文投入しない"
      ;;
    docs/refs/*)
      document_class="legacy_reference"
      knowledge_action="history_only"
      current_status="historical_or_uncertain"
      reason="旧正本、候補仕様、実装記録が混在するため履歴照会専用"
      case "$path" in
        *.json|*.zip|*.html|*.csv|*.sh|*実行記録*|*チェックリスト*|*作成プロンプト*|*実装プロンプト*|*中間*)
          knowledge_action="exclude"
          manual_review="no"
          reason="raw生成物、実行記録、checklist、promptはKnowledge原文投入対象外"
          ;;
        *仕様.md|*正本仕様.md|*設計*.md|*ガバナンス*.md)
          knowledge_action="distill"
          knowledge_target="candidate:legacy-spec-review"
          knowledge_type="spec"
          knowledge_roles="worker,coder"
          provider_scope="local_only"
          reason="現行仕様とcodeで再検証した要点だけを採用候補にする"
          ;;
      esac
      ;;
    docs/archive/*)
      document_class="archive"
      knowledge_action="history_only"
      current_status="historical"
      reason="削除判断や過去経緯の確認専用。通常Recallへ入れない"
      case "$path" in
        *.zip|*.json|*.html|*.csv|*.sh)
          knowledge_action="exclude"
          manual_review="no"
          reason="archive内のbinaryまたは生成物はKnowledge対象外"
          ;;
      esac
      ;;
    docs/00_引き継ぎ/*|docs/99_整理/*|docs/*引き継ぎ*|docs/レビュー結果.md|docs/復帰候補一覧.md|docs/更新候補一覧.md|docs/00_読む順番.md)
      document_class="handoff_and_governance"
      knowledge_action="history_only"
      current_status="historical_or_operational"
      reason="引き継ぎ、整理根拠、文書棚卸しの監査用"
      ;;
  esac

  case "$path" in
    *.json|*.zip|*.png|*.jpg|*.jpeg|*.gif|*.webp|*.pdf|*.parquet)
      if [[ "$knowledge_action" != "promote" ]]; then
        knowledge_action="exclude"
        knowledge_target=""
        knowledge_type=""
        knowledge_roles=""
        provider_scope="none"
        manual_review="no"
        reason="binaryまたは機械生成データのためKnowledge原文投入対象外"
      fi
      ;;
  esac

  contains_local_metadata="no"
  if rg -Iq '/home/nyukimi|C:\\Users\\nyuki|192\.168\.[0-9]+\.[0-9]+|10\.[0-9]+\.[0-9]+\.[0-9]+' "$path" 2>/dev/null; then
    contains_local_metadata="yes"
    local_count=$((local_count + 1))
  fi

  hash=$(sha256sum "$path" | awk '{print $1}')
  bytes=$(wc -c < "$path" | tr -d ' ')
  source_state="origin/main@${baseline}+docs-snapshot-20260715"

  write_row "$all_csv" \
    "$path" "$title" "$document_class" "$main_action" "$knowledge_action" \
    "$knowledge_target" "$knowledge_type" "$knowledge_roles" "$provider_scope" \
    "$canonical_source" "$current_status" "$superseded_by" "$contains_local_metadata" \
    "$manual_review" "$reason" "$source_state" "$hash" "$bytes" "$reviewed_at"

  if [[ "$knowledge_action" == "promote" || "$knowledge_action" == "distill" ]]; then
    write_row "$knowledge_csv" \
      "$path" "$title" "$knowledge_action" "$knowledge_target" "$knowledge_type" \
      "$knowledge_roles" "$provider_scope" "$canonical_source" "$current_status" \
      "$manual_review" "$reason" "$hash" "$reviewed_at"
  fi

  total=$((total + 1))
  case "$main_action" in
    rewrite) rewrite_count=$((rewrite_count + 1)) ;;
    remove) remove_count=$((remove_count + 1)) ;;
  esac
  case "$knowledge_action" in
    promote) promote_count=$((promote_count + 1)) ;;
    distill) distill_count=$((distill_count + 1)) ;;
    history_only) history_count=$((history_count + 1)) ;;
    exclude) exclude_count=$((exclude_count + 1)) ;;
  esac
done < <(find docs -type f ! -path 'docs/分類/*' -print0 | sort -z)

cat > "$summary_md" <<EOF
# 文書分類集計

- reviewed_at: $reviewed_at
- source_state: origin/main@$baseline + docs snapshot
- total: $total

## Main

| action | count |
| --- | ---: |
| rewrite | $rewrite_count |
| remove | $remove_count |

## Knowledge

| action | count |
| --- | ---: |
| promote | $promote_count |
| distill | $distill_count |
| history_only | $history_count |
| exclude | $exclude_count |

## Metadata review

| item | count |
| --- | ---: |
| local path / private network candidate | $local_count |

distillとlocal metadata候補は、Knowledge Wiki作成前に人間レビューを必須とする。
EOF

printf 'classified=%d rewrite=%d remove=%d promote=%d distill=%d history=%d exclude=%d local=%d\n' \
  "$total" "$rewrite_count" "$remove_count" "$promote_count" "$distill_count" \
  "$history_count" "$exclude_count" "$local_count"
