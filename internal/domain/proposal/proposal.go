package proposal

// Proposal はCoderからの提案を表す値オブジェクト
type Proposal struct {
	plan     string // 実装計画
	patch    string // パッチ（JSON/Markdown形式）
	risk     string // リスク評価
	costHint string // コストヒント
}

// NewProposal は新しいProposalを作成
func NewProposal(plan, patch, risk, costHint string) *Proposal {
	return &Proposal{
		plan:     plan,
		patch:    patch,
		risk:     risk,
		costHint: costHint,
	}
}

// Plan は実装計画を返す
func (p *Proposal) Plan() string {
	return p.plan
}

// Patch はパッチを返す
func (p *Proposal) Patch() string {
	return p.patch
}

// Risk はリスク評価を返す
func (p *Proposal) Risk() string {
	return p.risk
}

// CostHint はコストヒントを返す
func (p *Proposal) CostHint() string {
	return p.costHint
}

// IsValid はProposalが有効かを判定
func (p *Proposal) IsValid() bool {
	return p.plan != "" && p.patch != ""
}

// Reconstruct は永続化層からProposalを復元する（NewProposalのエイリアス）
func Reconstruct(plan, patch, risk, costHint string) *Proposal {
	return NewProposal(plan, patch, risk, costHint)
}
