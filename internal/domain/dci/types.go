package dci

import "time"

type Evidence struct {
	EvidenceID string  `json:"evidence_id"`
	SourceID   string  `json:"source_id,omitempty"`
	FilePath   string  `json:"file_path"`
	Heading    string  `json:"heading,omitempty"`
	LineStart  int     `json:"line_start"`
	LineEnd    int     `json:"line_end"`
	Snippet    string  `json:"snippet"`
	Reason     string  `json:"reason,omitempty"`
	Confidence float64 `json:"confidence"`
}

type EvidencePack struct {
	EventID      string     `json:"event_id"`
	Query        string     `json:"query"`
	Intent       string     `json:"intent,omitempty"`
	CorpusScope  []string   `json:"corpus_scope"`
	Evidence     []Evidence `json:"evidence"`
	DerivedTerms []string   `json:"derived_terms,omitempty"`
	Confidence   float64    `json:"confidence"`
	Limitations  []string   `json:"limitations,omitempty"`
}

type SourceMetadataRank struct {
	FilePath string  `json:"file_path"`
	SourceID string  `json:"source_id,omitempty"`
	Score    float64 `json:"score"`
	Reason   string  `json:"reason,omitempty"`
}

type SearchStep struct {
	StepNo       int       `json:"step_no"`
	Tool         string    `json:"tool"`
	CommandText  string    `json:"command_text,omitempty"`
	FilePath     string    `json:"file_path,omitempty"`
	ResultCount  int       `json:"result_count,omitempty"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type SearchTrace struct {
	EventID            string       `json:"event_id"`
	StartedAt          time.Time    `json:"started_at"`
	EndedAt            time.Time    `json:"ended_at"`
	Actor              string       `json:"actor"`
	Mode               string       `json:"mode"`
	UserQuery          string       `json:"user_query"`
	CorpusScope        []string     `json:"corpus_scope"`
	Steps              []SearchStep `json:"steps"`
	FinalEvidenceCount int          `json:"final_evidence_count"`
	Status             string       `json:"status"`
	ErrorMessage       string       `json:"error_message,omitempty"`
}

type SearchResult struct {
	Pack  EvidencePack `json:"pack"`
	Trace SearchTrace  `json:"trace"`
}
