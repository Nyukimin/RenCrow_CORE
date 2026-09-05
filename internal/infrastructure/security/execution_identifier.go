package security

import (
	"fmt"
	"sync/atomic"
	"time"
)

// actionIDSequence はポリシー実行Action識別子の一意性を担保する
var actionIDSequence atomic.Uint64

// nextActionID generates the identity of one policy-mediated Action. TaskID is
// supplied by the owner route and must never be generated here.
// time.Now().UnixNano() だけではクロック粒度が粗い環境
// （Windowsでは100ns〜1ms程度）で連続生成した識別子が衝突するため、
// 単調増加カウンタを併用する。
func nextActionID() string {
	now := time.Now().UnixNano()
	seq := actionIDSequence.Add(1)
	return fmt.Sprintf("act-%d-%d", now, seq)
}
