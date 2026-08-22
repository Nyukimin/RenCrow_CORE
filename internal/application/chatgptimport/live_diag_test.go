package chatgptimport

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestLiveDiagChatGPTPreflight(t *testing.T) {
	artifactPath := os.Getenv("RENCROW_LIVE_CHATGPT_ARTIFACT")
	if artifactPath == "" {
		t.Skip("RENCROW_LIVE_CHATGPT_ARTIFACT is not set")
	}
	artifact, err := os.Open(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	records, err := liveDiagRecordsReader(tar.NewReader(artifact))
	if err != nil {
		t.Fatal(err)
	}

	store, err := l1sqlite.NewL1SQLiteStore("/srv/rencrow/db/core/databases/conversation/l1_memory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetCommonRawSourceRoot("/srv/rencrow/db/core/memory/raw-sources"); err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope("live-diag-chatgpt", domaintool.ActorKindUser, "ren", "ren", []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domaintool.WithToolExecutionScope(context.Background(), scope)

	const exportID = "7b876db216bded4692c99aa764f90b600be1f9ad9a9a86597250802787b76585"
	scanner := bufio.NewScanner(records)
	scanner.Buffer(make([]byte, 64*1024), 68<<20)
	batch := make([]domainmemory.ChatGPTL3ImportRecord, 0, 100)
	line := 0
	batchIndex := 0
	flush := func() {
		if len(batch) == 0 {
			return
		}
		input := domainmemory.ChatGPTRawImportBatch{
			ExportID:       exportID,
			ManifestSHA256: "a15db4e1453dfd92e75fa06f0f404313cbfa9b14b73fa031b74134657e9cd5ab",
			ArtifactSHA256: "77a4a18b3cb657dacb1ccb1ed22a1878523c5a02c7e6ab2822f5e54a6c1d967a",
			SourceCount:    1751, SchemaVersion: RecordSchema, ConverterVersion: ConverterVersion,
			BatchIndex: batchIndex, BatchCount: 575, StartLine: line - len(batch) + 1,
			Records: append([]domainmemory.ChatGPTL3ImportRecord(nil), batch...),
		}
		result, callErr := store.ImportChatGPTRawBatch(ctx, "live-diag-chatgpt", "ren", "ren", input, false)
		if callErr != nil {
			t.Fatalf("batch=%d start=%d evidence=%s code=%q err=%v", batchIndex, input.StartLine, batch[0].EvidenceID, domainmemory.CommonRawErrorCodeOf(callErr), callErr)
		}
		if verifyErr := verifyMessagePreflightResult(result, "ren", input); verifyErr != nil {
			t.Fatalf("batch=%d start=%d evidence=%s result=%+v verify=%v", batchIndex, input.StartLine, batch[0].EvidenceID, result, verifyErr)
		}
		batch = batch[:0]
		batchIndex++
	}
	for scanner.Scan() {
		line++
		item, err := decodeServiceMessage(append([]byte(nil), scanner.Bytes()...), exportID)
		if err != nil {
			t.Fatalf("decode line=%d: %v", line, err)
		}
		batch = append(batch, item)
		if len(batch) == 100 {
			flush()
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	flush()
	if line != 54032 {
		t.Fatalf("lines=%d", line)
	}
}

func liveDiagRecordsReader(reader *tar.Reader) (io.Reader, error) {
	for {
		header, err := reader.Next()
		if err != nil {
			return nil, err
		}
		if header.Name == "records.jsonl" {
			return io.LimitReader(reader, header.Size), nil
		}
		if errors.Is(err, io.EOF) {
			return nil, errors.New("records.jsonl is missing")
		}
	}
}
