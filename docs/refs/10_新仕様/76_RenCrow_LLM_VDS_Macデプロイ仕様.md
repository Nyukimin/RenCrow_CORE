# 76 RenCrow_LLM VDS — Mac デプロイ仕様（索引）

## 1. 位置づけ

| 項目 | 内容 |
| --- | --- |
| 設計 | `74_Viewer音声直結LLM_Streaming仕様.md` |
| 実装 | `75_Viewer音声直結LLM_Streaming実装作業仕様.md` §5 PR-L1 |
| **正本（Mac 作業手順）** | [`RenCrow_LLM/docs/VDS_audio_session_Macデプロイ仕様.md`](../../../RenCrow_LLM/docs/VDS_audio_session_Macデプロイ仕様.md) |

本書は索引。Mac 上での作業手順・検証・復旧の詳細は **RenCrow_LLM 側正本**を参照する。

---

## 2. なぜ Mac 作業が必要か

VDS の LLM 接続先は **RenCrow_LLM Chat**（`:8081`）である。

PR-L1（`/v1/chat/audio/sessions`）は RenCrow_LLM のソースに含まれるが、**実行中 Chat プロセス**へ反映するには LLM ホスト上で `git pull` + `mlx-restart Chat` が必要。

picoclaw / Viewer 側（PR-P1, PR-V1）は Linux 側で完了済み。  
**残タスク = LLM ホスト（Mac）への PR-L1 反映**。

---

## 3. クイック手順（Mac）

```bash
cd ~/RenCrow/RenCrow_LLM
git pull
PYTHONPATH=src uv run python -m unittest tests.test_audio_session_contract -q
uv run mlx-restart Chat

curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/v1/chat/audio/sessions
# 期待: 426（404 ではない）
```

8081 が塞がれている場合は正本 §8.2 を参照。

---

## 4. Linux 側（picoclaw）の後続作業

Mac 合格後:

1. `RENCROW_LLM_CHAT_WS=ws://127.0.0.1:18091/...` を削除
2. `systemctl --user restart picoclaw.service`
3. `scripts/vds_e2e_probe.py` 再計測

詳細: `scripts/deploy_rencrow_llm_vds_pr_l1.md`
