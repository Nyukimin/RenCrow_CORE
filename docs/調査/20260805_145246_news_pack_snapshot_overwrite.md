# News Pack表示件数が87件から20件へ減る問題

## 事象

News Packを開くと直近24時間分を表示した後、件数が20件へ減った。

## 原因

- News Packの24時間APIとMemoryタブの`limit=20` APIが、同じ`state.memory.snapshot`へ応答全体を書き込んでいた。
- News Pack取得後にMemory APIが完了すると、`renderMemorySnapshot()`経由でNews Packも再描画され、20件へ置き換わった。
- `viewer.js`と`news-pack.js`のasset URLに以前の固定versionが残っており、既存ブラウザが修正前JSをcacheできる状態だった。

## 対応

- News Pack専用の`state.memory.newsPackSnapshot`を追加した。
- News Packの表示、成功応答、失敗時のclearを専用snapshotへ限定した。
- Memory snapshotの更新はNews Pack snapshotを変更しない。
- 2つのJS asset URLを`20260805-news-pack-snapshot`へ更新した。

## 検証

- News Pack関連Node test 4件成功。News Packが2件の状態でMemory snapshotを20件へ更新しても、News Pack表示が2件を維持する回帰testを含む。
- `go test ./internal/adapter/viewer`成功。
- webgather、sourcefetcher、l1sqlite、viewerの関連Go test成功。
- Node test全86件中、今回関連84件成功。既存IdleChat test 2件は`idleEsc is not defined`で失敗し、News Pack変更とは無関係。
- 稼働ViewerをFirefoxで開き、News Pack 88件を確認。その画面上で`refreshMemorySnapshot()`を実行し、Memory snapshotが20件になった後もNews Pack snapshotと表示は88件を維持した。さらに5秒後も88件を維持した。
- 稼働runtimeは`/health`が全check `ok`、`/ready`が`true`、`NRestarts=0`。
- service再起動はsystemdの正常なSIGTERMと`Shutdown complete`を経ている。

## 補足

Playwright CLIの既定Chromeは`/opt/google/chrome/chrome`が存在せず起動できなかったため、インストール済みのPlaywright Firefoxで同じViewerを検証した。
