package revenue

import "time"

type MarketResearchItem struct {
	ItemID         string    `json:"item_id"`
	SourcePlatform string    `json:"source_platform"`
	SourceURL      string    `json:"source_url,omitempty"`
	CreatorName    string    `json:"creator_name,omitempty"`
	Theme          string    `json:"theme,omitempty"`
	ProductName    string    `json:"product_name,omitempty"`
	Price          int       `json:"price,omitempty"`
	ObservedSignal string    `json:"observed_signal,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type SNSPostMetric struct {
	PostID        string    `json:"post_id"`
	Platform      string    `json:"platform"`
	PostedAt      time.Time `json:"posted_at,omitempty"`
	Title         string    `json:"title,omitempty"`
	Theme         string    `json:"theme,omitempty"`
	Impressions   int       `json:"impressions,omitempty"`
	Likes         int       `json:"likes,omitempty"`
	Reposts       int       `json:"reposts,omitempty"`
	Comments      int       `json:"comments,omitempty"`
	Saves         int       `json:"saves,omitempty"`
	ProfileClicks int       `json:"profile_clicks,omitempty"`
	LinkClicks    int       `json:"link_clicks,omitempty"`
	SalesCount    int       `json:"sales_count,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Product struct {
	ProductID    string    `json:"product_id"`
	ProductName  string    `json:"product_name"`
	ProductType  string    `json:"product_type,omitempty"`
	Price        int       `json:"price,omitempty"`
	Target       string    `json:"target,omitempty"`
	Pain         string    `json:"pain,omitempty"`
	Promise      string    `json:"promise,omitempty"`
	Deliverables string    `json:"deliverables,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

type CustomerVoice struct {
	VoiceID            string    `json:"voice_id"`
	CustomerID         string    `json:"customer_id,omitempty"`
	ProductID          string    `json:"product_id,omitempty"`
	VoiceType          string    `json:"voice_type,omitempty"`
	RawText            string    `json:"raw_text"`
	Summary            string    `json:"summary,omitempty"`
	UsableForMarketing bool      `json:"usable_for_marketing"`
	PermissionStatus   string    `json:"permission_status"`
	CreatedAt          time.Time `json:"created_at"`
}

type RevenueEvent struct {
	EventID       string    `json:"event_id"`
	TraceID       string    `json:"trace_id,omitempty"`
	OpportunityID string    `json:"opportunity_id,omitempty"`
	DeliveryID    string    `json:"delivery_id,omitempty"`
	EventType     string    `json:"event_type"`
	ProductID     string    `json:"product_id,omitempty"`
	Amount        int       `json:"amount,omitempty"`
	Channel       string    `json:"channel,omitempty"`
	CustomerID    string    `json:"customer_id,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Opportunity struct {
	OpportunityID   string    `json:"opportunity_id"`
	TraceID         string    `json:"trace_id,omitempty"`
	SourceKind      string    `json:"source_kind"`
	Title           string    `json:"title"`
	Summary         string    `json:"summary,omitempty"`
	TargetCustomer  string    `json:"target_customer,omitempty"`
	ExpectedRevenue int       `json:"expected_revenue,omitempty"`
	ExpectedCost    int       `json:"expected_cost,omitempty"`
	ExpectedProfit  int       `json:"expected_profit,omitempty"`
	ProfitMargin    float64   `json:"profit_margin,omitempty"`
	ReuseValue      float64   `json:"reuse_value,omitempty"`
	AutomationRate  float64   `json:"automation_rate,omitempty"`
	StrategicValue  float64   `json:"strategic_value,omitempty"`
	RiskScore       float64   `json:"risk_score,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type EconomicTask struct {
	TaskID        string    `json:"task_id"`
	TraceID       string    `json:"trace_id,omitempty"`
	OpportunityID string    `json:"opportunity_id"`
	WorkstreamID  string    `json:"workstream_id,omitempty"`
	AgentID       string    `json:"agent_id"`
	TaskKind      string    `json:"task_kind"`
	Status        string    `json:"status"`
	ExpectedValue float64   `json:"expected_value,omitempty"`
	Risk          float64   `json:"risk,omitempty"`
	Cost          float64   `json:"cost,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type EconomicReflection struct {
	ReflectionID   string    `json:"reflection_id"`
	TraceID        string    `json:"trace_id,omitempty"`
	OpportunityID  string    `json:"opportunity_id"`
	RevenueEventID string    `json:"revenue_event_id,omitempty"`
	Outcome        string    `json:"outcome"`
	NetProfit      int       `json:"net_profit,omitempty"`
	Lessons        []string  `json:"lessons,omitempty"`
	NextActions    []string  `json:"next_actions,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type DailyRoutineReport struct {
	ReportID            string    `json:"report_id"`
	WorkstreamID        string    `json:"workstream_id,omitempty"`
	Date                string    `json:"date"`
	Summary             string    `json:"summary,omitempty"`
	MarketResearch      int       `json:"market_research_count"`
	SNSPosts            int       `json:"sns_post_count"`
	Products            int       `json:"product_count"`
	CustomerVoices      int       `json:"customer_voice_count"`
	RevenueEvents       int       `json:"revenue_event_count"`
	PaidCustomers       int       `json:"paid_customer_count"`
	BlockedDecisions    int       `json:"blocked_decision_count"`
	SuggestedActions    []string  `json:"suggested_actions,omitempty"`
	Status              string    `json:"status"`
	ExternalSendApplied bool      `json:"external_send_applied"`
	CreatedAt           time.Time `json:"created_at"`
}

type ChannelDraft struct {
	DraftID             string    `json:"draft_id"`
	TraceID             string    `json:"trace_id,omitempty"`
	OpportunityID       string    `json:"opportunity_id,omitempty"`
	WorkstreamID        string    `json:"workstream_id,omitempty"`
	Channel             string    `json:"channel"`
	Subject             string    `json:"subject,omitempty"`
	Body                string    `json:"body"`
	SourceReportID      string    `json:"source_report_id,omitempty"`
	ExternalSendApplied bool      `json:"external_send_applied"`
	CreatedAt           time.Time `json:"created_at"`
}

type ExternalSendApplyRecord struct {
	ApplyID             string    `json:"apply_id"`
	TraceID             string    `json:"trace_id,omitempty"`
	DeliveryID          string    `json:"delivery_id,omitempty"`
	DraftID             string    `json:"draft_id"`
	DecisionID          string    `json:"decision_id"`
	Channel             string    `json:"channel"`
	Destination         string    `json:"destination,omitempty"`
	ChannelAdapter      string    `json:"channel_adapter,omitempty"`
	ApplyStatus         string    `json:"apply_status"`
	SendResult          string    `json:"send_result"`
	FailureReason       string    `json:"failure_reason,omitempty"`
	PostSendVerified    bool      `json:"post_send_verified"`
	PostSendEvidence    string    `json:"post_send_evidence,omitempty"`
	ExternalSendApplied bool      `json:"external_send_applied"`
	CreatedAt           time.Time `json:"created_at"`
}

type Delivery struct {
	DeliveryID       string    `json:"delivery_id"`
	TraceID          string    `json:"trace_id"`
	OpportunityID    string    `json:"opportunity_id,omitempty"`
	TaskID           string    `json:"task_id,omitempty"`
	WorkstreamID     string    `json:"workstream_id,omitempty"`
	ArtifactID       string    `json:"artifact_id,omitempty"`
	PolicyDecisionID string    `json:"policy_decision_id,omitempty"`
	DeliveryKind     string    `json:"delivery_kind"`
	Status           string    `json:"status"`
	Target           string    `json:"target,omitempty"`
	Result           string    `json:"result,omitempty"`
	Evidence         string    `json:"evidence,omitempty"`
	ExternalAction   bool      `json:"external_action"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
}

type EthicsCheck struct {
	Allowed  bool     `json:"allowed"`
	Reasons  []string `json:"reasons,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type PolicyDecisionRequest struct {
	DecisionID   string    `json:"decision_id,omitempty"`
	TraceID      string    `json:"trace_id,omitempty"`
	DecisionType string    `json:"decision_type"`
	SubjectID    string    `json:"subject_id,omitempty"`
	Description  string    `json:"description,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
}

type PolicyDecisionResult struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
}

type PolicyDecisionRecord struct {
	DecisionID   string    `json:"decision_id"`
	TraceID      string    `json:"trace_id,omitempty"`
	DecisionType string    `json:"decision_type"`
	SubjectID    string    `json:"subject_id,omitempty"`
	Description  string    `json:"description,omitempty"`
	Status       string    `json:"status"`
	Reasons      []string  `json:"reasons,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}
