# Viewer Live Verification

## Purpose

RenCrow Viewer の UI / interaction / runtime 表示を、実ブラウザ、DOM、必要な screenshot / log evidence で確認する。

## When to Use

- Viewer UI、CSS、HTML、JS、Viewer API を変更した。
- ユーザーが「見た目」「クリックできない」「表示が崩れる」「Viewerで確認」と依頼した。
- Ops / System / Jobs / Chat / Timeline など Viewer tab の到達性や情報量を確認する。

## Required Context

- live base URL: `http://127.0.0.1:18790/viewer`
- relevant rules: `rules/rules_viewer_ui.md`
- service rebuild skill: `skills/core/rencrow-service-rebuild-restart`

## Procedure

1. 変更対象の Viewer route / tab / DOM id を特定する。
2. live service に反映が必要なら `rencrow-service-rebuild-restart` を先に使う。
3. desktop viewport で対象 tab を開く。
4. narrow / mobile viewport でも確認する。
5. 初期表示が要約中心で、長文 error / URL / raw log が layout を押し広げないことを確認する。
6. fixed input bar、toast、overlay、live-mode、lipsync 周辺は `pointer-events`、`z-index`、`position`、`background`、`border`、`box-shadow` を確認する。
7. 必要なら screenshot、console error、network error、DOM snapshot を保存する。

## Verification

- desktop と mobile で主要 UI が重ならない。
- 対象操作がクリック可能。
- console に今回変更起因の error がない。
- API failure がある場合は、表示と audit / log の境界が明確。

## Safety

- DOM 要素の存在だけで完了扱いしない。
- UI変更では static test のみを完了条件にしない。
- screenshot / DOM evidence を原因理解前に消さない。

