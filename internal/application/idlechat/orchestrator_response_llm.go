package idlechat

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func (o *IdleChatOrchestrator) generateIdleLLM(provider llm.LLMProvider, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if provider == nil {
		return llm.GenerateResponse{}, fmt.Errorf("idlechat LLM provider is nil")
	}
	timeout := idleChatLLMGenerateTimeout
	role := "none"
	if len(req.Messages) > 0 {
		role = strings.TrimSpace(req.Messages[len(req.Messages)-1].Role)
		if role == "" {
			role = "unknown"
		}
	}
	baseCtx := o.idleRunContext()
	if timeout <= 0 {
		resp, err := provider.Generate(baseCtx, req)
		if err == nil {
			logIdleRaw(fmt.Sprintf("llm.generate role=%s", role), resp.Content)
			log.Printf("[IdleChat][llm] role=%s max_tokens=%d finish=%q tokens=%d", role, req.MaxTokens, resp.FinishReason, resp.TokensUsed)
		}
		return resp, err
	}
	ctx, cancel := context.WithTimeout(baseCtx, timeout)
	defer cancel()
	resp, err := provider.Generate(ctx, req)
	if err == nil {
		logIdleRaw(fmt.Sprintf("llm.generate role=%s", role), resp.Content)
		log.Printf("[IdleChat][llm] role=%s max_tokens=%d finish=%q tokens=%d", role, req.MaxTokens, resp.FinishReason, resp.TokensUsed)
	}
	return resp, err
}

func fallbackIdleResponse(speaker, topic, latestOther string, turn int) string {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		topic = "この話題"
	}
	topicShort := truncate(topic, 42)
	other := truncate(strings.TrimSpace(latestOther), 34)
	isShiro := strings.EqualFold(strings.TrimSpace(speaker), "shiro")
	variants := turn % 8
	if isShiro {
		switch variants {
		case 0:
			return fmt.Sprintf("そのお題なら、まず%sを一つの場面に絞ると話が進みます。棚や通路みたいな具体物を置くと、見え方が安定します。", topicShort)
		case 1:
			if other != "" {
				return fmt.Sprintf("今の「%s」は入口として使えますね。次は誰が何を見落としたのか、一点だけ決めると輪郭が出ます。", other)
			}
			return fmt.Sprintf("%sは抽象のままだと散るので、最初の発見を一つ決めたいです。小さな違和感から始めるのがよさそうです。", topicShort)
		case 2:
			return fmt.Sprintf("%sでは、人物の動きより先に場所のルールを決めると整理できます。何が普通で、何が一度だけズレたのかを見たいですね。", topicShort)
		case 3:
			return fmt.Sprintf("ここは結論を急がず、%sを触れる物に落としましょう。音、匂い、置き場所のどれか一つが次の手がかりになります。", topicShort)
		case 4:
			return fmt.Sprintf("%sを追うなら、誰か一人の習慣を決めるのがよさそうです。同じ動きが一度だけ崩れると、会話の焦点になります。", topicShort)
		case 5:
			return fmt.Sprintf("視点を少し狭めると、%sは観察記録として扱えます。最初に残る痕跡を一つ選ぶと、次の問いが自然に出ます。", topicShort)
		case 6:
			return fmt.Sprintf("その方向なら、場所の明るさや足音の変化を使えます。%sを説明ではなく、誰かが気づく瞬間に寄せたいですね。", topicShort)
		default:
			return fmt.Sprintf("いま必要なのは、%sの中で変化する一点を決めることです。人、物、時間のどれが先にズレるかで展開が変わります。", topicShort)
		}
	}
	switch variants {
	case 0:
		return fmt.Sprintf("えー、%sって、最初に小さな違和感を一つ置くと一気に見えそうだね。たとえば誰かがいつもと違う場所で立ち止まる場面から始めたいな。", topicShort)
	case 1:
		if other != "" {
			return fmt.Sprintf("その「%s」って手がかり、けっこう効きそう。じゃあ次は、それを最初に見つける人の表情から決めてみない？", other)
		}
		return fmt.Sprintf("いいじゃん、%sなら人の癖が見える瞬間から入りたいな。何気ない動きが、あとで意味を持つ感じにしたい。", topicShort)
	case 2:
		return fmt.Sprintf("%s、ただ説明するより一場面で見せたいね。古い照明が一瞬だけ揺れる、みたいな合図があると話が動きそう。", topicShort)
	case 3:
		return fmt.Sprintf("気になるのは、%sの中で誰が最初に違和感へ気づくかだね。そこを決めたら、会話も自然に前へ進みそう。", topicShort)
	case 4:
		return fmt.Sprintf("いいね、%sなら最初の手がかりをすごく小さくしたいな。落ちている紙片とか、匂いが一瞬変わるとか、そのくらいが効きそう。", topicShort)
	case 5:
		return fmt.Sprintf("%sって、誰かのいつもの癖がズレるだけで話になりそう。そこから『今日は何か違う』って空気を作りたい。", topicShort)
	case 6:
		return fmt.Sprintf("それなら、%sを一人の目線で追うのがよさそうだね。見慣れた場所の一箇所だけが変わっていて、そこから会話が動く感じ。", topicShort)
	default:
		return fmt.Sprintf("じゃあ%sは、最後に大きな説明を置くより、最初に触れる物を決めたいな。その物が誰の記憶につながるかで広げられそう。", topicShort)
	}
}
