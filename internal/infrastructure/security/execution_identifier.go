package security

import (
	"fmt"
	"sync/atomic"
	"time"
)

// executionIDSequence はポリシー実行識別子の一意性を担保する
var executionIDSequence atomic.Uint64

// nextExecutionIdentifiers はポリシー実行の job_id と action_id を生成する
//
// この組は internal/infrastructure/persistence/execution の actionKey()
// （jobID + "::" + actionID）で map キーになるため、衝突すると監査記録が
// 上書きされて失われる。time.Now().UnixNano() だけではクロック粒度が粗い環境
// （Windowsでは100ns〜1ms程度）で連続生成した識別子が衝突するため、
// 単調増加カウンタを併用する。
func nextExecutionIdentifiers() (jobID string, actionID string) {
	now := time.Now().UnixNano()
	seq := executionIDSequence.Add(1)
	return fmt.Sprintf("job-%d-%d", now, seq), fmt.Sprintf("act-%d-%d", now, seq)
}
