package task

import (
	"errors"
	"fmt"
)

// InvalidStatusTransitionError は許可されないTask状態遷移の拒否を表す。
// エラー文言は既存のログ・監視が頼る形を変えない。
type InvalidStatusTransitionError struct {
	From Status
	To   Status
}

func (e *InvalidStatusTransitionError) Error() string {
	return fmt.Sprintf("invalid status transition: %s -> %s", e.From, e.To)
}

// AlreadyTerminal は、err が「遷移元が既に終端状態のTaskへ別の状態を書こうとした」
// ことによる拒否かを返す。孤児回収など別経路が先にTaskを決着させた場合の信号で、
// 非終端状態からの無効遷移や無関係なエラーは決着済みとはみなさない。
func AlreadyTerminal(err error) bool {
	var transition *InvalidStatusTransitionError
	return errors.As(err, &transition) && IsTerminal(transition.From)
}
