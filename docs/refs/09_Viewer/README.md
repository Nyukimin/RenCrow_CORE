# Viewer 文書

このディレクトリは RenCrow Viewer の仕様、UI ルールに基づく生成プロンプト、検討用モックを置く場所である。

## 読み順

1. `Viewer仕様.md`
   - Viewer の役割、主要 route、表示契約、検証方針を確認する。
2. `../../rules/rules_viewer_ui.md`
   - 新 UI / 新タブを作るときの必須ルールを確認する。
3. `Viewer新UIタブ生成プロンプト.md`
   - Home / Chat / Develop / Instructions / Reports などの常用 UI を LLM に作らせるときに使う。
4. `Viewer添付入力仕様.md`
   - Viewer 入力欄から添付を扱う場合に確認する。

## 位置づけ

- `rules/rules_viewer_ui.md` は作業時に守るルールである。
- `Viewer新UIタブ生成プロンプト.md` は、そのルールを LLM に渡しやすい形へ整えたプロンプトである。
- `new_ui_chat.html` は検討用モックであり、実装の正本ではない。

Viewer 本体へ組み込む場合は、`internal/adapter/viewer/viewer.html`、`internal/adapter/viewer/assets/css/`、`internal/adapter/viewer/assets/js/` の既存責務分割に従う。
