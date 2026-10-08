# nativeharnessclient testdata

このdirectoryのfileは、RenCrow_Harness設計v0.2.2の`examples/`から複製した合成test vectorです。

- 出所: RenCrow_Harness設計v0.2.2（zip SHA-256 `802a00297bf8c0ed01e4c0ce254f7663aadbf30da7163305e88625969f098c53`）
- 取得日: 2026-10-07
- 用途: ContextBlockのrevision式（[アーキテクチャ概要](../../../../docs/04_アーキテクチャ概要.md#rencrow_harnessへ渡すcontextblock)）と
  OriginProofのMAC式（[安全・自動実行・データ方針](../../../../docs/07_安全・自動実行・データ方針.md#rencrow_harness委譲の安全境界)）を、
  別実装が同じ出力を再現できることの確認。式の正本はCORE docsであり、本directoryは正本ではない。

| file | 設計package内の元path | SHA-256 |
| --- | --- | --- |
| `context_revision_vectors.json` | `examples/context_revision_vectors.json` | `deed418e24d00e16e13870f99b24f0fb7a5c774ec303d5b9793287a0e344c5c8` |
| `core_start.json` | `examples/core_start.json` | `158568f1367c28f82c6927e73ef168218ad087f6d9fb1d1f6b55eb2f45fdb1ad` |
| `origin_proof_vector.json` | `examples/origin_proof_vector.json` | `a1f437bff89520beb801ab43ccaf395b251edcba03e363f32be3edbb4a1f4cb0` |
| `synthetic_origin_key.hex` | `examples/wire/synthetic_origin_key.hex` | `46268c8f3e4d5291cfd71ffc476063a199beff7f3a5f87a5552a6e3985eccd90` |

## 取扱い

- 全て合成データであり、実機の観測値ではない。
- `synthetic_origin_key.hex`と`origin_proof_vector.json`の`key_hex`は、test vector専用の公開された合成鍵である。
  実機・開発機・deploymentの鍵として使わない（`LoadKeyFile`と起動時検査はこの鍵を拒否し、`ParseKey`だけがvector再現のために受理する）。
  実鍵はworkspace外のowner専用fileへ、明示手順で別に生成する。
- 実装は、RenCrow_HarnessのGo codeや参照codecを読まず・複製せず・importしない。CORE正本の式とこのvectorだけを根拠に
  独立して再現する（cross-moduleの独立再現が受入条件）。
- vectorの内容を変更しない。設計packageが更新された場合は、出所とSHA-256を更新して差し替える。
