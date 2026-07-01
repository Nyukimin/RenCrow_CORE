package otelexport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Event struct {
	Name       string            `json:"name"`
	TraceID    string            `json:"trace_id,omitempty"`
	SpanID     string            `json:"span_id,omitempty"`
	ParentID   string            `json:"parent_id,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Timestamp  string            `json:"timestamp,omitempty"`
}

type ExportRequest struct {
	Endpoint   string  `json:"endpoint,omitempty"`
	Service    string  `json:"service,omitempty"`
	Events     []Event `json:"events"`
	SampleRate float64 `json:"sample_rate,omitempty"`
	DryRun     bool    `json:"dry_run"`
}

type ExportReport struct {
	Status       string         `json:"status"`
	EndpointSet  bool           `json:"endpoint_set"`
	Exported     int            `json:"exported"`
	Dropped      int            `json:"dropped"`
	RedactedKeys []string       `json:"redacted_keys,omitempty"`
	Payload      map[string]any `json:"payload,omitempty"`
}

type Service struct {
	endpoint string
	client   *http.Client
	now      func() time.Time
}

func NewService(endpoint string) *Service {
	return &Service{
		endpoint: strings.TrimSpace(endpoint),
		client:   &http.Client{Timeout: 10 * time.Second},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) Export(ctx context.Context, req ExportRequest) (ExportReport, error) {
	if s == nil {
		return ExportReport{}, fmt.Errorf("otel export service unavailable")
	}
	if len(req.Events) == 0 {
		return ExportReport{}, fmt.Errorf("events is required")
	}
	endpoint := firstNonEmpty(req.Endpoint, s.endpoint)
	sampleRate := req.SampleRate
	if sampleRate <= 0 {
		sampleRate = 1
	}
	if sampleRate > 1 {
		sampleRate = 1
	}
	events, dropped := sampleEvents(req.Events, sampleRate)
	payload, redacted := s.buildPayload(firstNonEmpty(req.Service, "rencrow"), events)
	report := ExportReport{
		Status:       "preview",
		EndpointSet:  endpoint != "",
		Exported:     len(events),
		Dropped:      dropped,
		RedactedKeys: redacted,
		Payload:      payload,
	}
	if endpoint == "" || req.DryRun {
		return report, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ExportReport{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return ExportReport{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(httpReq)
	if err != nil {
		return ExportReport{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ExportReport{}, fmt.Errorf("otel endpoint returned status %d", resp.StatusCode)
	}
	report.Status = "exported"
	report.Payload = nil
	return report, nil
}

func (s *Service) buildPayload(service string, events []Event) (map[string]any, []string) {
	now := s.now().UTC().Format(time.RFC3339Nano)
	spans := make([]map[string]any, 0, len(events))
	var redacted []string
	for i, event := range events {
		attrs := make([]map[string]string, 0, len(event.Attributes)+1)
		attrs = append(attrs, map[string]string{"key": "service.name", "value": service})
		for key, value := range event.Attributes {
			if isSecretKey(key) {
				value = "[REDACTED]"
				redacted = appendUnique(redacted, key)
			}
			attrs = append(attrs, map[string]string{"key": key, "value": value})
		}
		spans = append(spans, map[string]any{
			"name":        firstNonEmpty(event.Name, fmt.Sprintf("rencrow.event.%d", i+1)),
			"trace_id":    firstNonEmpty(event.TraceID, fmt.Sprintf("trace-%d", i+1)),
			"span_id":     firstNonEmpty(event.SpanID, fmt.Sprintf("span-%d", i+1)),
			"parent_id":   event.ParentID,
			"kind":        firstNonEmpty(event.Kind, "internal"),
			"start_time":  firstNonEmpty(event.Timestamp, now),
			"end_time":    firstNonEmpty(event.Timestamp, now),
			"attributes":  attrs,
			"schema_hint": "otlp-json-lite",
		})
	}
	return map[string]any{
		"resourceSpans": []map[string]any{{
			"resource": map[string]any{"attributes": []map[string]string{{"key": "service.name", "value": service}}},
			"scopeSpans": []map[string]any{{
				"scope": map[string]string{"name": "rencrow"},
				"spans": spans,
			}},
		}},
	}, redacted
}

func sampleEvents(events []Event, sampleRate float64) ([]Event, int) {
	if sampleRate >= 1 {
		return append([]Event(nil), events...), 0
	}
	keepEvery := int(1 / sampleRate)
	if keepEvery < 2 {
		keepEvery = 2
	}
	out := make([]Event, 0, len(events))
	for i, event := range events {
		if i%keepEvery == 0 {
			out = append(out, event)
		}
	}
	return out, len(events) - len(out)
}

func isSecretKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "secret") || strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "api_key")
}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
