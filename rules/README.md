# RenCrow_CORE rules

CORE固有の制約だけを所有する。全repository共通の作業方針は、global設定から参照するEcoSystemのAGENTS.mdが唯一の正本。共通Skillもこの境界を上書きしない。

- CORE作業の入口: [AGENTS.md](../AGENTS.md)
- 作業条件から選ぶ詳細: [task-rules.md](task-rules.md)の該当節
- 製品仕様: [docs/README.md](../docs/README.md)
- 指示変更: [配置規定](rules_instruction_placement.md)
- path固有制約: [対象path](rules_path_scoped_constraints.md)

既存の`rules/common/`はCORE-local補足の互換pathであり、全module共通の配布元ではない。該当するCORE作業からだけ読む。他moduleへ複製せず、共通方針と重なる定義は共通正本を継承する。新しい制約はCORE固有ならここ、全module共通ならEcoSystemへ置く。
