package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modulechat "github.com/Nyukimin/picoclaw_multiLLM/modules/chat"
	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type fakeChatModuleService struct {
	input modulechat.Input
}

func (s *fakeChatModuleService) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "chat", Status: core.HealthReady, Ready: true}
}

func (s *fakeChatModuleService) DecideRoute(_ context.Context, input modulechat.Input) (modulechat.RouteDecision, error) {
	s.input = input
	return modulechat.RouteDecision{Route: modulechat.RouteWorker, Reason: "rule dictionary match"}, nil
}

func (s *fakeChatModuleService) Respond(context.Context, modulechat.Input, modulechat.RuntimePorts) (modulechat.Output, error) {
	return modulechat.Output{}, nil
}

func TestHandleModuleChatRouteDecision(t *testing.T) {
	service := &fakeChatModuleService{}
	handler := handleModuleChatRouteDecision(service)
	body := bytes.NewBufferString(`{"session_id":"s1","channel":"viewer","user_id":"u1","text":"実装して"}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/modules/chat/route", body)
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if service.input.Text != "実装して" || service.input.Channel != "viewer" {
		t.Fatalf("input was not mapped: %+v", service.input)
	}
	var got modulechat.RouteReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got.Route != modulechat.RouteWorker || got.Reason != "rule dictionary match" {
		t.Fatalf("unexpected route response: %+v", got)
	}
	if got.Health.Status != core.HealthReady {
		t.Fatalf("health was not included: %+v", got.Health)
	}
	if got.Health.CheckedAt.IsZero() {
		t.Fatalf("health checked_at was not set: %+v", got.Health)
	}
}

func TestHandleModuleChatRouteDecisionUnavailable(t *testing.T) {
	handler := handleModuleChatRouteDecision(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/modules/chat/route", bytes.NewBufferString(`{"text":"hi"}`))
	handler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}
