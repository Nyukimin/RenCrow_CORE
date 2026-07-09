package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/coderloop"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/patch"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const (
	defaultLoopMaxTurns       = 8
	defaultLoopMaxConsecFails = 2
)

// CoderAgentWithLoop は多ターンループに対応した CoderAgent
type CoderAgentWithLoop interface {
	CoderAgent
	GenerateWithContext(ctx context.Context, messages []llm.Message) (string, error)
}

// CoderLoopExecutor は CoderLoop を実行する
type CoderLoopExecutor struct {
	coder           CoderAgentWithLoop
	workerExecution workerObservationExecutor
	systemPrompt    string
	maxTurns        int
	eventEmitter    func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string)
}

// workerObservationExecutor は観測実行とパッチ実行の両方を持つ
type workerObservationExecutor interface {
	ExecuteObservation(ctx context.Context, actions []coderloop.ObservationAction) ([]coderloop.ObservationActionResult, error)
	ExecuteProposal(ctx context.Context, jobID task.JobID, p *proposal.Proposal) (*patch.PatchExecutionResult, error)
}

// NewCoderLoopExecutor は CoderLoopExecutor を生成する
func NewCoderLoopExecutor(
	coder CoderAgentWithLoop,
	worker workerObservationExecutor,
	systemPrompt string,
	eventEmitter func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string),
) *CoderLoopExecutor {
	return &CoderLoopExecutor{
		coder:           coder,
		workerExecution: worker,
		systemPrompt:    systemPrompt,
		maxTurns:        defaultLoopMaxTurns,
		eventEmitter:    eventEmitter,
	}
}

// WithMaxTurns はループ上限を変更する
func (e *CoderLoopExecutor) WithMaxTurns(n int) *CoderLoopExecutor {
	if n > 0 {
		e.maxTurns = n
	}
	return e
}

// LoopResult はループ完了後の結果
type LoopResult struct {
	FinalReport  *coderloop.FinalReportMessage
	Summary      string
	ChangedFiles []string
	Partial      bool // 上限到達などで途中終了した場合 true
}

// Execute はエージェントループを実行する
func (e *CoderLoopExecutor) Execute(ctx context.Context, req CodeExecutionRequest) (CodeExecutionResponse, error) {
	log.Printf("[CoderLoop] start job=%s route=%s", req.JobID, req.Route)
	e.emit("agent.start", "coder_loop", "mio", "CoderLoop 開始", req)

	result, err := e.runLoop(ctx, req)
	if err != nil {
		e.emit("agent.response", "coder_loop", "mio", "CoderLoop エラー: "+err.Error(), req)
		return CodeExecutionResponse{}, err
	}

	summary := e.formatLoopResult(result)
	e.emit("agent.report", "coder_loop", "mio", summary, req)
	return buildProposalHandledResponse(summary), nil
}

func (e *CoderLoopExecutor) runLoop(ctx context.Context, req CodeExecutionRequest) (*LoopResult, error) {
	// 会話履歴初期化
	messages := []llm.Message{
		{Role: "system", Content: e.systemPrompt},
		{Role: "user", Content: req.Task.UserMessage()},
	}

	consecutiveFails := 0
	var lastReport *coderloop.FinalReportMessage

	for turn := 1; turn <= e.maxTurns; turn++ {
		log.Printf("[CoderLoop] turn=%d job=%s", turn, req.JobID)

		// Coder 推論
		content, err := e.coder.GenerateWithContext(ctx, messages)
		if err != nil {
			return nil, fmt.Errorf("coder turn %d failed: %w", turn, err)
		}

		// Coder 出力を履歴に追記
		messages = append(messages, llm.Message{Role: "assistant", Content: content})

		// CoderMessage パース
		msg, err := coderloop.ParseCoderMessage(content)
		if err != nil {
			log.Printf("[CoderLoop] parse error turn=%d: %v", turn, err)
			e.emit("agent.response", "coder_loop", "mio",
				fmt.Sprintf("[turn %d] Coder 出力を解析できません: %v", turn, err), req)
			// パース失敗は連続失敗カウント対象
			consecutiveFails++
			if consecutiveFails >= defaultLoopMaxConsecFails {
				return &LoopResult{Summary: fmt.Sprintf("連続エラー（%d回）のためループ中断", consecutiveFails), Partial: true}, nil
			}
			// Coder に再試行を求めるメッセージを追加
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: fmt.Sprintf(`{"type":"observation","turn":%d,"results":[{"action":"parse","target":"response","status":"error","output":"JSON が見つかりません。必ず有効な JSON オブジェクトで応答してください。"}]}`, turn),
			})
			continue
		}

		e.emit("agent.response", "coder_loop", "shiro",
			fmt.Sprintf("[turn %d] type=%s", turn, msg.Type), req)

		// メッセージ種別ごとの処理
		switch msg.Type {

		case coderloop.TypeFinalReport:
			lastReport = msg.FinalReport
			log.Printf("[CoderLoop] final_report received job=%s turn=%d", req.JobID, turn)
			return &LoopResult{
				FinalReport:  lastReport,
				Summary:      lastReport.Summary,
				ChangedFiles: lastReport.ChangedFiles,
			}, nil

		case coderloop.TypeReadRequest:
			obs, err := e.executeObservations(ctx, turn, msg.ReadRequest.Actions, req)
			if err != nil {
				consecutiveFails++
			} else {
				consecutiveFails = 0
			}
			messages = append(messages, llm.Message{Role: "user", Content: obs})

		case coderloop.TypePlan:
			// plan は観測なし。Coder の次のターンへ
			consecutiveFails = 0
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: fmt.Sprintf(`{"type":"observation","turn":%d,"results":[{"action":"plan_ack","target":"plan","status":"ok","output":"計画を受け取りました。次のステップへ進んでください。"}]}`, turn),
			})

		case coderloop.TypePatchProposal:
			obs, execErr := e.executePatchProposal(ctx, turn, msg.PatchProposal, req)
			if execErr != nil {
				consecutiveFails++
			} else {
				consecutiveFails = 0
			}
			messages = append(messages, llm.Message{Role: "user", Content: obs})

		case coderloop.TypeTestRequest:
			obs, err := e.executeObservations(ctx, turn, msg.TestRequest.Actions, req)
			if err != nil {
				consecutiveFails++
			} else {
				consecutiveFails = 0
			}
			messages = append(messages, llm.Message{Role: "user", Content: obs})

		case coderloop.TypeRevisionRequest:
			// 追加調査アクションがあれば実行
			if len(msg.RevisionRequest.Actions) > 0 {
				obs, err := e.executeObservations(ctx, turn, msg.RevisionRequest.Actions, req)
				if err != nil {
					consecutiveFails++
				} else {
					consecutiveFails = 0
				}
				messages = append(messages, llm.Message{Role: "user", Content: obs})
			} else {
				consecutiveFails = 0
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: fmt.Sprintf(`{"type":"observation","turn":%d,"results":[{"action":"revision_ack","target":"revision","status":"ok","output":"修正依頼を受け取りました。パッチを修正してください。"}]}`, turn),
				})
			}
		}

		// 連続失敗チェック
		if consecutiveFails >= defaultLoopMaxConsecFails {
			return &LoopResult{
				Summary: fmt.Sprintf("連続エラー（%d回）のためループ中断", consecutiveFails),
				Partial: true,
			}, nil
		}
	}

	// ターン上限到達
	summary := "上限ターン数に達したため終了（final_report 未受信）"
	if lastReport != nil {
		summary = lastReport.Summary
	}
	return &LoopResult{Summary: summary, Partial: true}, nil
}

// executeObservations は WorkerAction スライスを観測実行して observation JSON を返す
func (e *CoderLoopExecutor) executeObservations(
	ctx context.Context,
	turn int,
	actions []coderloop.WorkerAction,
	req CodeExecutionRequest,
) (string, error) {
	obsActions := coderloop.ActionsFromWorkerActions(actions)
	results, err := e.workerExecution.ExecuteObservation(ctx, obsActions)
	obs := coderloop.NewObservationResult(turn, results)
	json := obs.ToJSON()
	e.emit("worker.result", "worker", "coder_loop", truncate(json, 300), req)
	return json, err
}

// executePatchProposal はパッチ案を Worker に適用させて observation JSON を返す
func (e *CoderLoopExecutor) executePatchProposal(
	ctx context.Context,
	turn int,
	pp *coderloop.PatchProposalMessage,
	req CodeExecutionRequest,
) (string, error) {
	p := proposal.NewProposal(pp.Intent, pp.Patch, "", "")
	e.emit("worker.request", "coder_loop", "worker",
		fmt.Sprintf("[turn %d] patch_proposal 適用中: %s", turn, pp.Intent), req)

	result, err := e.workerExecution.ExecuteProposal(ctx, req.Task.JobID(), p)
	if err != nil {
		obs := coderloop.NewObservationResult(turn, []coderloop.ObservationActionResult{
			coderloop.NewObservationActionResult("apply_patch", pp.Intent, "", err),
		})
		return obs.ToJSON(), err
	}

	statusStr := "ok"
	if !result.Success {
		statusStr = "error"
	}
	obs := coderloop.NewObservationResult(turn, []coderloop.ObservationActionResult{
		coderloop.NewObservationActionResult("apply_patch", pp.Intent, result.Summary, func() error {
			if result.Success {
				return nil
			}
			return fmt.Errorf("patch execution failed: %s", result.FailureReason)
		}()),
	})
	_ = statusStr
	return obs.ToJSON(), nil
}

// formatLoopResult はループ結果を人間が読みやすい文字列にフォーマットする
func (e *CoderLoopExecutor) formatLoopResult(r *LoopResult) string {
	if r == nil {
		return "CoderLoop: 結果なし"
	}
	var sb strings.Builder
	if r.Partial {
		sb.WriteString("⚠️ CoderLoop: 途中終了\n")
	} else {
		sb.WriteString("✅ CoderLoop: 完了\n")
	}
	sb.WriteString("\n## Summary\n")
	sb.WriteString(r.Summary)
	if len(r.ChangedFiles) > 0 {
		sb.WriteString("\n\n## Changed Files\n")
		for _, f := range r.ChangedFiles {
			sb.WriteString("- " + f + "\n")
		}
	}
	if r.FinalReport != nil && len(r.FinalReport.TestsRun) > 0 {
		sb.WriteString("\n## Tests\n")
		for _, t := range r.FinalReport.TestsRun {
			sb.WriteString("- " + t + "\n")
		}
	}
	if r.FinalReport != nil && len(r.FinalReport.RemainingRisks) > 0 {
		sb.WriteString("\n## Remaining Risks\n")
		for _, risk := range r.FinalReport.RemainingRisks {
			sb.WriteString("- " + risk + "\n")
		}
	}
	return sb.String()
}

func (e *CoderLoopExecutor) emit(eventType, from, to, content string, req CodeExecutionRequest) {
	if e.eventEmitter == nil {
		return
	}
	e.eventEmitter(eventType, from, to, content, req.Route.String(), req.JobID, req.SessionID, req.Channel, req.ChatID)
}
