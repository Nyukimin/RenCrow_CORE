package contract

import (
	"fmt"
	"strings"

	domaincontract "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/contract"
)

// NormalizeRequest converts a free-form request into an executable contract.
func NormalizeRequest(raw string) (domaincontract.Contract, error) {
	return NormalizeRequestWithRoute(raw, "")
}

// NormalizeRequestWithRoute converts a free-form request into an executable contract
// with optional route-specific acceptance and verification defaults.
func NormalizeRequestWithRoute(raw string, route string) (domaincontract.Contract, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return domaincontract.Contract{}, fmt.Errorf("request is empty")
	}

	lower := strings.ToLower(text)
	if strings.Contains(lower, "tts") || strings.Contains(text, "音声") {
		c := domaincontract.Contract{
			Goal: text,
			Acceptance: []string{
				"音声ファイル生成成功",
				"実再生成功",
				"実行証跡保存成功",
			},
			Constraints: []string{
				"破壊的コマンドを実行しない",
				"機密情報を外部送信しない",
			},
			Artifacts: []string{
				"tts/generated_audio.*",
				"execution_report.json",
			},
			Verification: []string{
				"TTS CLIまたはAPI呼び出しが0終了",
				"音声再生またはデコード検証が成功",
			},
			Rollback: []string{
				"追加設定を元に戻す",
				"一時生成物を削除して再実行可能状態に戻す",
			},
		}
		return c, c.Validate()
	}

	upperRoute := strings.ToUpper(strings.TrimSpace(route))
	c := domaincontract.Contract{
		Goal: text,
		Acceptance: []string{
			"依頼の受入条件を満たす実行結果が得られる",
		},
		Constraints: []string{
			"破壊的コマンドを実行しない",
		},
		Artifacts: []string{
			"execution_report.json",
		},
		Verification: []string{
			"主要アクションの実行結果が成功",
		},
		Rollback: []string{
			"変更を元に戻せる手順を残す",
		},
	}
	switch upperRoute {
	case "CODE", "CODE1", "CODE2", "CODE3", "CODE4":
		c.Acceptance = []string{
			"実装変更または実行可能な patch が生成・適用される",
			"Worker/Coder の実行が最後まで完了する",
		}
		c.Verification = []string{
			"主要な apply が成功する",
			"最終報告が成功状態で返る",
		}
	case "OPS":
		c.Acceptance = []string{
			"運用タスクが最後まで実行される",
		}
		c.Verification = []string{
			"主要アクションの実行結果が成功",
			"最終報告が成功状態で返る",
		}
	case "PLAN", "ANALYZE", "RESEARCH":
		c.Acceptance = []string{
			"依頼に対する実行可能な報告が返る",
		}
		c.Verification = []string{
			"応答が空でない",
			"最終報告が失敗状態でない",
		}
	}
	return c, c.Validate()
}
