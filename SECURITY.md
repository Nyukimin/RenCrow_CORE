# Security Policy

## Supported version

Security fixes are applied to the current `main` branch. Older tags are historical snapshots unless a release note states otherwise.

## Reporting a vulnerability

Repository owner の GitHub profile から private contact を利用するか、GitHub の Private vulnerability reporting が有効な場合は Security タブから報告してください。公開 Issue には exploit 手順、secret、個人情報、未修正 endpoint を記載しないでください。

報告には、影響範囲、再現条件、期待する安全な挙動、可能なら最小の再現例を含めてください。受領後、再現確認、影響評価、修正方針、公開時期を調整します。

## Operational safety

- API key、token、private key を YAML やログへ保存しない。
- Viewer や管理 API を外部公開する場合は、network boundary と認証を別途設計する。
- Tool、Browser、Worker の書込・外部送信範囲を allowlist と approval で制限する。
- 公開、送信、請求、契約、価格決定を人間承認なしに実行しない。
