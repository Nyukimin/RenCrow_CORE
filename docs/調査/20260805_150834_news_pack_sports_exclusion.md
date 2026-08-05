# News Pack Sports除外

## 目的

News PackでSports記事を今後収集・表示せず、既存本文は削除しない。

## 変更前

- 直近24時間のNews Packは88件で、このうちSportsは14件だった。
- Sportsはすべて`rss:news:nhk-sports`から取得されていた。
- source URLは`https://www.nhk.or.jp/rss/news/cat7.xml`で、enabledだった。

## 対応

- Source Registryの`rss:news:nhk-sports`をdisabledへ変更した。
- News Packの24時間クエリから`category=sports`を除外した。
- 既存Sports記事と取得済み本文はL1 DBから削除していない。

## 検証

- `go test ./internal/infrastructure/persistence/conversation/l1sqlite ./internal/adapter/viewer`成功。
- webgather、sourcefetcher、l1sqlite、viewerの関連Go test成功。
- 稼働Source Registryで`rss:news:nhk-sports enabled=false`を確認した。
- 起動後の全feed sweepは14 sourcesから13 sourcesへ減少した。
- 稼働News Pack APIは直近24時間74件、Sports 0件を返した。
- DBのread-only確認ではSports 54行を保持し、物理削除していない。
- runtimeは`/health`が全check `ok`、`/ready`が`true`、`NRestarts=0`。
- 再起動はsystemdの正常なSIGTERMと`Shutdown complete`を経ている。
