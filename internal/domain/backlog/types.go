package backlog

type Item struct {
	ItemID         string   `json:"item_id"`
	Kind           string   `json:"kind"`
	Title          string   `json:"title"`
	Body           string   `json:"body,omitempty"`
	Source         string   `json:"source"`
	Owner          string   `json:"owner,omitempty"`
	Status         string   `json:"status"`
	Priority       string   `json:"priority"`
	Tags           []string `json:"tags,omitempty"`
	Implementer    string   `json:"implementer,omitempty"`
	Implementation string   `json:"implementation,omitempty"`
	TestResult     string   `json:"test_result,omitempty"`
	CheckOK        bool     `json:"check_ok"`
	CheckedBy      string   `json:"checked_by,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}
