package main

import (
	"context"
	"strings"
	"testing"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

func TestRuntimeDailySourceBriefReadURLRejectsInsufficientBody(t *testing.T) {
	fetcher := &fakeWebGatherFetcher{resp: modulewebgather.FetchResponse{
		URL: "https://example.com/article", Status: "ok", ExtractedText: "見出しに近い短い断片だけです。",
	}}
	research := &runtimeDailySourceBriefResearch{fetcher: fetcher}

	_, err := research.ReadURL(context.Background(), "https://example.com/article")
	if err == nil || !strings.Contains(err.Error(), "短すぎる") {
		t.Fatalf("短い断片を本文として扱ってはならない: %v", err)
	}
}

func TestRuntimeDailySourceBriefReadURLReturnsDirectlyExtractedBody(t *testing.T) {
	body := strings.Repeat("一次情報の本文です。", 20)
	fetcher := &fakeWebGatherFetcher{resp: modulewebgather.FetchResponse{
		URL: "https://example.com/article", FinalURL: "https://example.com/final", Status: "ok", Title: "公式記事", ExtractedText: body,
	}}
	research := &runtimeDailySourceBriefResearch{fetcher: fetcher}

	doc, err := research.ReadURL(context.Background(), "https://example.com/article")
	if err != nil {
		t.Fatalf("ReadURL: %v", err)
	}
	if doc.URL != "https://example.com/final" || doc.Text != body {
		t.Fatalf("直接取得本文 = %+v", doc)
	}
	if !fetcher.req.Refresh || fetcher.req.StoreStaging || !fetcher.req.StoreStagingSet {
		t.Fatalf("日次処理は特定URLを直接再取得し、stagingへ保存しない: %+v", fetcher.req)
	}
}
