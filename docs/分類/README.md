# RenCrow CORE 文書保存分類

このディレクトリは、2026-07-15 時点の RenCrow CORE 文書を、Public Main と Knowledge 利用の2軸で分類した保存索引である。

元文書は相対リンク、Git履歴、検索性を保つため、archive branch内では元のpathから移動しない。`残すもの`と`外すもの`は実体の移動先ではなく、分類結果を人間が読むための索引である。

## ファイル

- `全資料一覧.csv`: 文書ごとのMain処理、Knowledge処理、状態、hash、判断理由
- `Knowledge候補一覧.csv`: `promote`または`distill`に分類した文書だけの一覧
- `集計.md`: 分類件数
- `残すもの/README.md`: Public仕様へ再構成する情報源
- `外すもの/README.md`: Mainから外してarchiveに保存する資料群
- `generate_classification_manifest.sh`: 一覧の再生成手順

## 判定値

### main_action

- `rewrite`: 内容をPublic向け文書へ再構成する
- `remove`: Mainから外し、archive branchにのみ保存する

### knowledge_action

- `promote`: 現行仕様として短いKnowledge Wikiへ採用可能
- `distill`: 現行code/正本で再検証し、要点だけをWiki候補にする
- `history_only`: 履歴照会時だけ参照し、通常Recallへ入れない
- `exclude`: raw log、生成物、巨大解析、重複、binary等。Knowledgeへ投入しない

## 注意

- `distill`は原文投入を意味しない。
- `history_only`と`exclude`は通常のRecall対象にしない。
- local path、private network情報、credential候補は蒸留時に除去する。
- Mainへの反映は`.public-docs-allowlist`を正とし、このarchive branchをmergeしない。
