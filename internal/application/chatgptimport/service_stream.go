package chatgptimport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type sourceLayout struct {
	sourceCount   int
	fileCount     int
	chunkCount    int
	sourceBatches int
}

func buildImportPlan(ctx context.Context, bundle *VerifiedBundle, options normalizedServiceOptions) (importPlan, error) {
	if bundle == nil {
		return importPlan{}, fmt.Errorf("%w: verified bundle is missing", errChatGPTImportPlan)
	}
	domainBindingValue, err := domainBinding(bundle.Binding())
	if err != nil {
		return importPlan{}, err
	}
	verifyOptions, err := options.verify.normalized()
	if err != nil {
		return importPlan{}, err
	}
	layout, err := planSourceLayout(ctx, bundle, domainBindingValue, verifyOptions, options)
	if err != nil {
		return importPlan{}, err
	}
	plan := importPlan{
		binding: domainBindingValue, sourceCount: layout.sourceCount,
		fileCount: layout.fileCount, chunkCount: layout.chunkCount,
		objectCount: domainBindingValue.SourceObjectCount, messageCount: domainBindingValue.MessageCount,
		sourceBatches: layout.sourceBatches, maxJSONLineSize: verifyOptions.MaxJSONLineBytes,
	}
	if err := validateConservativeMessageSizes(ctx, bundle, plan, options); err != nil {
		return importPlan{}, err
	}

	messageBatchCount := (plan.messageCount + options.maxMessageRecords - 1) / options.maxMessageRecords
	if messageBatchCount <= 0 {
		return importPlan{}, fmt.Errorf("%w: message batch count is invalid", errChatGPTImportPlan)
	}
	var jobCount int
	for attempt := 0; attempt < 16; attempt++ {
		candidateBatchCount := plan.sourceBatches + messageBatchCount
		actualBatchCount, actualJobs, err := countMessageBatches(ctx, bundle, plan, options, candidateBatchCount)
		if err != nil {
			return importPlan{}, err
		}
		if actualBatchCount == messageBatchCount {
			jobCount = actualJobs
			plan.messageBatches = actualBatchCount
			plan.batchCount = plan.sourceBatches + actualBatchCount
			plan.jobCount = jobCount
			return plan, nil
		}
		messageBatchCount = actualBatchCount
		if messageBatchCount <= 0 {
			return importPlan{}, fmt.Errorf("%w: message batch count did not converge", errChatGPTImportPlan)
		}
	}
	return importPlan{}, fmt.Errorf("%w: message batch sizing did not converge", errChatGPTImportPlan)
}

func planSourceLayout(ctx context.Context, bundle *VerifiedBundle, binding domainmemory.ChatGPTImportBinding, verifyOptions Options, options normalizedServiceOptions) (sourceLayout, error) {
	index, err := bundle.OpenSourceIndex()
	if err != nil {
		return sourceLayout{}, fmt.Errorf("%w: open verified source index", domainmemory.ErrChatGPTImportUnavailable)
	}
	defer index.Close()
	reader := bufio.NewReaderSize(index, 64*1024)
	result := sourceLayout{}
	var batchRecords int
	var batchPayload int64
	for {
		line, readErr := readCanonicalJSONLine(reader, verifyOptions.MaxJSONLineBytes)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) || errors.Is(readErr, ErrBounds) {
				return sourceLayout{}, readErr
			}
			return sourceLayout{}, fmt.Errorf("%w: read verified source index", errChatGPTImportSourceChanged)
		}
		source, decodeErr := decodeSourceIndexRecord(line, verifyOptions.ChunkBytes)
		if decodeErr != nil {
			return sourceLayout{}, fmt.Errorf("%w: source index metadata changed", errChatGPTImportSourceChanged)
		}
		if err := ctx.Err(); err != nil {
			return sourceLayout{}, err
		}
		result.fileCount++
		result.chunkCount += len(source.Chunks)
		if len(source.Chunks) == 0 {
			if source.Bytes != 0 {
				return sourceLayout{}, fmt.Errorf("%w: empty source file has nonzero size", errChatGPTImportSourceChanged)
			}
			batchRecords, batchPayload, result.sourceBatches = addSourceLayoutRecord(batchRecords, batchPayload, 0, result.sourceBatches, options)
			result.sourceCount++
			continue
		}
		for _, chunk := range source.Chunks {
			if chunk.Bytes > options.maxPayloadBytes {
				return sourceLayout{}, fmt.Errorf("%w: source chunk exceeds batch payload bound", ErrBounds)
			}
			batchRecords, batchPayload, result.sourceBatches = addSourceLayoutRecord(batchRecords, batchPayload, chunk.Bytes, result.sourceBatches, options)
			result.sourceCount++
		}
	}
	if batchRecords > 0 {
		result.sourceBatches++
	}
	if result.fileCount != binding.SourceFileCount || result.chunkCount != binding.SourceChunkCount {
		return sourceLayout{}, fmt.Errorf("%w: source index counts differ from binding", errChatGPTImportSourceChanged)
	}
	if result.sourceCount == 0 || result.sourceBatches == 0 {
		return sourceLayout{}, fmt.Errorf("%w: source index contains no source records", errChatGPTImportSourceChanged)
	}
	return result, nil
}

func addSourceLayoutRecord(records int, payload int64, recordBytes int64, batches int, options normalizedServiceOptions) (int, int64, int) {
	if records > 0 && (records >= options.maxSourceRecords || recordBytes > options.maxPayloadBytes-payload) {
		batches++
		records = 0
		payload = 0
	}
	records++
	payload += recordBytes
	return records, payload, batches
}

type messageBatch struct {
	batch    domainmemory.ChatGPTRawImportBatch
	payload  int64
	jobCount int
}

func validateConservativeMessageSizes(ctx context.Context, bundle *VerifiedBundle, plan importPlan, options normalizedServiceOptions) error {
	_, _, err := streamMessageBatches(ctx, bundle, plan, options, plan.messageCount, 1, func(item messageBatch) error {
		conservative := item.batch
		conservative.BatchIndex = plan.messageCount - 1
		conservative.BatchCount = plan.messageCount
		payload, marshalErr := messageBatchPayloadBytes(conservative)
		if marshalErr != nil {
			return marshalErr
		}
		if payload > options.maxPayloadBytes {
			return fmt.Errorf("%w: one ChatGPT message payload exceeds the batch bound", ErrBounds)
		}
		return nil
	})
	return err
}

func countMessageBatches(ctx context.Context, bundle *VerifiedBundle, plan importPlan, options normalizedServiceOptions, batchCount int) (int, int, error) {
	actual, jobs, err := streamMessageBatches(ctx, bundle, plan, options, batchCount, options.maxMessageRecords, nil)
	return actual, jobs, err
}

func streamMessageBatches(ctx context.Context, bundle *VerifiedBundle, plan importPlan, options normalizedServiceOptions, batchCount, maxRecords int, callback func(messageBatch) error) (int, int, error) {
	if batchCount <= 0 || maxRecords <= 0 || maxRecords > chatGPTImportMaxBatchRecords {
		return 0, 0, fmt.Errorf("%w: message stream bounds are invalid", errChatGPTImportPlan)
	}
	records, err := bundle.OpenRecords()
	if err != nil {
		return 0, 0, fmt.Errorf("%w: open verified message stream", domainmemory.ErrChatGPTImportUnavailable)
	}
	defer records.Close()
	reader := bufio.NewReaderSize(records, 64*1024)
	current := make([]domainmemory.ChatGPTL3ImportRecord, 0, maxRecords)
	currentStartLine := 1
	currentIndex := 0
	lineNumber := 0
	jobCount := 0
	emitted := 0
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		batch := domainmemory.ChatGPTRawImportBatch{
			ExportID: plan.binding.ExportID, ManifestSHA256: plan.binding.ManifestSHA256,
			ArtifactSHA256: plan.binding.ArtifactSHA256, SourceCount: plan.sourceCount,
			SchemaVersion: plan.binding.SchemaVersion, ConverterVersion: plan.binding.ConverterVersion,
			BatchIndex: currentIndex, BatchCount: batchCount, StartLine: currentStartLine,
			Records: append([]domainmemory.ChatGPTL3ImportRecord(nil), current...),
		}
		payload, payloadErr := messageBatchPayloadBytes(batch)
		if payloadErr != nil {
			return payloadErr
		}
		if payload > options.maxPayloadBytes {
			return fmt.Errorf("%w: ChatGPT message batch payload exceeds the bound", ErrBounds)
		}
		if callback != nil {
			if err := callback(messageBatch{batch: batch, payload: payload, jobCount: countCurrentBranchJobs(current)}); err != nil {
				return err
			}
		}
		emitted++
		current = current[:0]
		currentIndex++
		return nil
	}
	for {
		line, readErr := readCanonicalJSONLine(reader, plan.maxJSONLineSize)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) || errors.Is(readErr, ErrBounds) {
				return 0, 0, readErr
			}
			return 0, 0, fmt.Errorf("%w: read verified message stream", errChatGPTImportSourceChanged)
		}
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		lineNumber++
		item, decodeErr := decodeServiceMessage(line, plan.binding.ExportID)
		if decodeErr != nil {
			return 0, 0, fmt.Errorf("%w: verified message record is invalid", ErrInvalidBundle)
		}
		if item.Role == "user" && item.OnCurrentBranch {
			jobCount++
		}
		if len(current) == 0 {
			currentStartLine = lineNumber
		}
		if len(current) >= maxRecords {
			if err := flush(); err != nil {
				return 0, 0, err
			}
			currentStartLine = lineNumber
		}
		current = append(current, item)
		candidate := domainmemory.ChatGPTRawImportBatch{
			ExportID: plan.binding.ExportID, ManifestSHA256: plan.binding.ManifestSHA256,
			ArtifactSHA256: plan.binding.ArtifactSHA256, SourceCount: plan.sourceCount,
			SchemaVersion: plan.binding.SchemaVersion, ConverterVersion: plan.binding.ConverterVersion,
			BatchIndex: currentIndex, BatchCount: batchCount, StartLine: currentStartLine,
			Records: current,
		}
		payload, payloadErr := messageBatchPayloadBytes(candidate)
		if payloadErr != nil {
			return 0, 0, payloadErr
		}
		if payload > options.maxPayloadBytes {
			if len(current) == 1 {
				return 0, 0, fmt.Errorf("%w: one ChatGPT message payload exceeds the batch bound", ErrBounds)
			}
			current = current[:len(current)-1]
			if err := flush(); err != nil {
				return 0, 0, err
			}
			currentStartLine = lineNumber
			current = append(current, item)
			candidate.Records = current
			candidate.BatchIndex = currentIndex
			candidate.StartLine = currentStartLine
			payload, payloadErr = messageBatchPayloadBytes(candidate)
			if payloadErr != nil {
				return 0, 0, payloadErr
			}
			if payload > options.maxPayloadBytes {
				return 0, 0, fmt.Errorf("%w: one ChatGPT message payload exceeds the batch bound", ErrBounds)
			}
		}
	}
	if err := flush(); err != nil {
		return 0, 0, err
	}
	if lineNumber != plan.messageCount || emitted == 0 {
		return 0, 0, fmt.Errorf("%w: message stream count differs from binding", ErrInvalidBundle)
	}
	return emitted, jobCount, nil
}

func messageBatchPayloadBytes(batch domainmemory.ChatGPTRawImportBatch) (int64, error) {
	var total int64
	for index := range batch.Records {
		payload, err := domainmemory.MarshalChatGPTRawPayload(batch, index)
		if err != nil {
			return 0, fmt.Errorf("%w: marshal ChatGPT Raw payload", domainmemory.ErrChatGPTImportInvalid)
		}
		if int64(len(payload)) > chatGPTImportMaxBatchPayload || total > chatGPTImportMaxBatchPayload-int64(len(payload)) {
			return chatGPTImportMaxBatchPayload + 1, nil
		}
		total += int64(len(payload))
	}
	return total, nil
}

func decodeServiceMessage(line []byte, exportID string) (domainmemory.ChatGPTL3ImportRecord, error) {
	record, err := decodeArtifactRecord(line, exportID)
	if err != nil {
		return domainmemory.ChatGPTL3ImportRecord{}, err
	}
	conversationCreatedAt, err := parseServiceTimestamp(record.ConversationCreatedAt)
	if err != nil {
		return domainmemory.ChatGPTL3ImportRecord{}, err
	}
	conversationUpdatedAt, err := parseServiceTimestamp(record.ConversationUpdatedAt)
	if err != nil {
		return domainmemory.ChatGPTL3ImportRecord{}, err
	}
	messageCreatedAt, err := parseServiceTimestamp(record.MessageCreatedAt)
	if err != nil {
		return domainmemory.ChatGPTL3ImportRecord{}, err
	}
	item := domainmemory.ChatGPTL3ImportRecord{
		Format: record.Format, ExportID: record.ExportID, EvidenceID: record.EvidenceID,
		ConversationID: record.ConversationID, ConversationTitle: record.ConversationTitle,
		ConversationCreatedAt: conversationCreatedAt, ConversationUpdatedAt: conversationUpdatedAt,
		NodeID: record.NodeID, ParentNodeID: record.ParentNodeID,
		ChildNodeIDs: append([]string(nil), record.ChildNodeIDs...), OnCurrentBranch: record.OnCurrentBranch,
		MessageID: record.MessageID, MessageCreatedAt: messageCreatedAt, Role: record.Role,
		ContentType: record.ContentType, Text: record.Text,
		Content: append([]byte(nil), record.Content...), Metadata: append([]byte(nil), record.Metadata...),
	}
	if err := domainmemory.ValidateChatGPTL3ImportRecord(item); err != nil {
		return domainmemory.ChatGPTL3ImportRecord{}, err
	}
	return item, nil
}

func parseServiceTimestamp(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("timestamp is not strict RFC3339Nano")
	}
	return parsed.UTC(), nil
}

func countCurrentBranchJobs(records []domainmemory.ChatGPTL3ImportRecord) int {
	count := 0
	for _, record := range records {
		if record.Role == "user" && record.OnCurrentBranch {
			count++
		}
	}
	return count
}

type sourceRecordSpec struct {
	path       string
	fileIndex  int
	chunkIndex int
	chunk      chunkReference
	empty      bool
}

type sourceBatch struct {
	input   domainmemory.CommonRawIntakeRequest
	records int
}

type sourceManifestProvenance struct {
	Adapter    string                            `json:"adapter"`
	Binding    domainmemory.ChatGPTImportBinding `json:"binding"`
	BatchIndex int                               `json:"batch_index"`
	BatchCount int                               `json:"batch_count"`
}

type sourceRecordProvenance struct {
	Adapter        string                            `json:"adapter"`
	Binding        domainmemory.ChatGPTImportBinding `json:"binding"`
	SourcePath     string                            `json:"source_path"`
	FileIndex      int                               `json:"file_index"`
	ChunkIndex     int                               `json:"chunk_index"`
	ChunkSHA256    string                            `json:"chunk_sha256"`
	ChunkBytes     int64                             `json:"chunk_bytes"`
	SyntheticEmpty bool                              `json:"synthetic_empty"`
}

func streamSourceBatches(ctx context.Context, bundle *VerifiedBundle, plan importPlan, verifyOptions Options, options normalizedServiceOptions, callback func(sourceBatch) error) error {
	index, err := bundle.OpenSourceIndex()
	if err != nil {
		return fmt.Errorf("%w: open verified source index", domainmemory.ErrChatGPTImportUnavailable)
	}
	defer index.Close()
	reader := bufio.NewReaderSize(index, 64*1024)
	current := make([]sourceRecordSpec, 0, options.maxSourceRecords)
	var currentPayload int64
	batchIndex := 0
	fileIndex := 0
	emittedRecords := 0
	emittedChunks := 0
	flush := func() error {
		if len(current) == 0 {
			return nil
		}
		batch, err := makeSourceBatch(ctx, bundle, plan, current, batchIndex, plan.sourceBatches)
		if err != nil {
			return err
		}
		if err := callback(batch); err != nil {
			return err
		}
		batchIndex++
		current = current[:0]
		currentPayload = 0
		return nil
	}
	for {
		line, readErr := readCanonicalJSONLine(reader, verifyOptions.MaxJSONLineBytes)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) || errors.Is(readErr, ErrBounds) {
				return readErr
			}
			return fmt.Errorf("%w: read verified source index", errChatGPTImportSourceChanged)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		source, decodeErr := decodeSourceIndexRecord(line, verifyOptions.ChunkBytes)
		if decodeErr != nil {
			return fmt.Errorf("%w: source index metadata changed", errChatGPTImportSourceChanged)
		}
		if fileIndex >= plan.fileCount {
			return fmt.Errorf("%w: source file count changed", errChatGPTImportSourceChanged)
		}
		if len(source.Chunks) == 0 {
			if source.Bytes != 0 {
				return fmt.Errorf("%w: empty source file size changed", errChatGPTImportSourceChanged)
			}
			spec := sourceRecordSpec{path: source.Path, fileIndex: fileIndex, chunkIndex: 0, empty: true, chunk: chunkReference{SHA256: domainmemory.SHA256Hex(nil), Bytes: 0}}
			if len(current) >= options.maxSourceRecords {
				if err := flush(); err != nil {
					return err
				}
			}
			current = append(current, spec)
			emittedRecords++
		} else {
			for chunkIndex, chunk := range source.Chunks {
				if chunk.Bytes > options.maxPayloadBytes {
					return fmt.Errorf("%w: source chunk exceeds batch payload bound", ErrBounds)
				}
				if len(current) >= options.maxSourceRecords || chunk.Bytes > options.maxPayloadBytes-currentPayload {
					if err := flush(); err != nil {
						return err
					}
				}
				current = append(current, sourceRecordSpec{path: source.Path, fileIndex: fileIndex, chunkIndex: chunkIndex, chunk: chunk})
				currentPayload += chunk.Bytes
				emittedRecords++
				emittedChunks++
			}
		}
		fileIndex++
	}
	if err := flush(); err != nil {
		return err
	}
	if fileIndex != plan.fileCount || emittedRecords != plan.sourceCount || emittedChunks != plan.chunkCount || batchIndex != plan.sourceBatches {
		return fmt.Errorf("%w: source stream counts changed", errChatGPTImportSourceChanged)
	}
	return nil
}

func makeSourceBatch(ctx context.Context, bundle *VerifiedBundle, plan importPlan, specs []sourceRecordSpec, batchIndex, batchCount int) (sourceBatch, error) {
	records := make([]domainmemory.CommonRawRecord, 0, len(specs))
	for _, spec := range specs {
		content := []byte{}
		if !spec.empty {
			var err error
			content, err = readVerifiedSourceObject(ctx, bundle, spec.chunk)
			if err != nil {
				return sourceBatch{}, err
			}
		}
		provenanceBytes, err := json.Marshal(sourceRecordProvenance{
			Adapter: chatGPTImportAdapter, Binding: plan.binding, SourcePath: spec.path,
			FileIndex: spec.fileIndex, ChunkIndex: spec.chunkIndex, ChunkSHA256: spec.chunk.SHA256,
			ChunkBytes: spec.chunk.Bytes, SyntheticEmpty: spec.empty,
		})
		if err != nil {
			return sourceBatch{}, fmt.Errorf("%w: marshal source provenance", errChatGPTImportPlan)
		}
		recordID := "chatgpt-source:" + domainmemory.SHA256Hex([]byte(strings.Join([]string{
			plan.binding.ExportID, spec.path, strconv.Itoa(spec.fileIndex), strconv.Itoa(spec.chunkIndex), spec.chunk.SHA256,
		}, "\x00")))
		records = append(records, domainmemory.CommonRawRecord{
			SourceRecordID: recordID, ParentID: plan.binding.ExportID, ThreadID: spec.path,
			Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: "source", ContentType: chatGPTImportSourceContent,
			OccurredAt: time.Unix(0, 0).UTC(), Content: content, ContentSHA256: domainmemory.SHA256Hex(content),
			Provenance: string(provenanceBytes), Rights: "owner", License: "private",
		})
	}
	manifestProvenance, err := json.Marshal(sourceManifestProvenance{Adapter: chatGPTImportAdapter, Binding: plan.binding, BatchIndex: batchIndex, BatchCount: batchCount})
	if err != nil {
		return sourceBatch{}, fmt.Errorf("%w: marshal source manifest provenance", errChatGPTImportPlan)
	}
	manifest := domainmemory.CommonRawManifest{
		ContractVersion: domainmemory.CommonRawContractVersion, SourceType: chatGPTImportSourceType,
		SourceIdentity: plan.binding.ExportID, SourceCount: len(records), AssetCount: 0,
		SchemaVersion: plan.binding.SchemaVersion, ConverterVersion: plan.binding.ConverterVersion,
		Sensitivity: domainmemory.CommonRawPrivateSensitivity, Rights: "owner", License: "private",
		Provenance: string(manifestProvenance), AllowEmpty: false,
	}
	manifest.ManifestSHA256, err = domainmemory.CommonRawInputHash(manifest, records, nil)
	if err != nil {
		return sourceBatch{}, fmt.Errorf("%w: hash source intake", errChatGPTImportPlan)
	}
	return sourceBatch{input: domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: records}, records: len(records)}, nil
}

func readVerifiedSourceObject(ctx context.Context, bundle *VerifiedBundle, chunk chunkReference) ([]byte, error) {
	object, err := bundle.OpenObject(chunk.SHA256)
	if err != nil {
		return nil, fmt.Errorf("%w: verified source object unavailable", domainmemory.ErrCommonRawObject)
	}
	defer object.Close()
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: object}, chunk.Bytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != chunk.Bytes || domainmemory.SHA256Hex(data) != chunk.SHA256 {
		return nil, fmt.Errorf("%w: verified source object changed", errChatGPTImportSourceChanged)
	}
	return data, nil
}

func (s *Service) preflightMessages(ctx context.Context, request ImportRequest, bundle *VerifiedBundle, plan importPlan, options normalizedServiceOptions) error {
	emitted, jobs, err := streamMessageBatches(ctx, bundle, plan, options, plan.batchCount, options.maxMessageRecords, func(item messageBatch) error {
		result, callErr := s.store.ImportChatGPTRawBatch(ctx, request.RequestID, request.OwnerID, request.ActorID, item.batch, false)
		if callErr != nil {
			return callErr
		}
		return verifyMessagePreflightResult(result, request.OwnerID, item.batch)
	})
	if err == nil && (emitted != plan.messageBatches || jobs != plan.jobCount) {
		return fmt.Errorf("%w: message preflight plan changed", errChatGPTImportSourceChanged)
	}
	return err
}

func (s *Service) applySources(ctx context.Context, request ImportRequest, bundle *VerifiedBundle, plan importPlan, options normalizedServiceOptions, progress *importProgress) error {
	verifyOptions, err := options.verify.normalized()
	if err != nil {
		return err
	}
	return streamSourceBatches(ctx, bundle, plan, verifyOptions, options, func(batch sourceBatch) error {
		receipt, callErr := s.store.IntakeCommonRaw(ctx, request.RequestID, request.OwnerID, request.ActorID, batch.input)
		if callErr != nil {
			return callErr
		}
		if err := verifySourceReceipt(receipt, request.RequestID, request.OwnerID, batch.input); err != nil {
			return err
		}
		progress.counts.RawCount += batch.records
		return nil
	})
}

func (s *Service) applyMessages(ctx context.Context, request ImportRequest, bundle *VerifiedBundle, plan importPlan, options normalizedServiceOptions, progress *importProgress) error {
	emitted, jobs, err := streamMessageBatches(ctx, bundle, plan, options, plan.batchCount, options.maxMessageRecords, func(item messageBatch) error {
		result, callErr := s.store.ImportChatGPTRawBatch(ctx, request.RequestID, request.OwnerID, request.ActorID, item.batch, true)
		if callErr != nil {
			return callErr
		}
		if err := verifyMessageResult(result, request.RequestID, request.OwnerID, item.batch, item.jobCount); err != nil {
			return err
		}
		progress.counts.RawCount += len(item.batch.Records)
		progress.counts.ProjectionCount += result.Projected + result.Existing
		progress.counts.JobCount += item.jobCount
		return nil
	})
	if err == nil && (emitted != plan.messageBatches || jobs != plan.jobCount) {
		return fmt.Errorf("%w: message apply plan changed", errChatGPTImportSourceChanged)
	}
	return err
}

func verifyMessagePreflightResult(result domainmemory.ChatGPTRawImportResult, ownerID string, batch domainmemory.ChatGPTRawImportBatch) error {
	expected, err := expectedChatGPTMessageRaw(ownerID, batch)
	if err != nil || result.Validated != len(batch.Records) || result.ExternalManifestSHA256 != batch.ManifestSHA256 || result.ArtifactSHA256 != batch.ArtifactSHA256 || result.ManifestID != expected.manifestID || result.InternalManifestSHA256 != expected.manifestSHA || result.SourceCount != batch.SourceCount || result.SchemaVersion != batch.SchemaVersion || result.ConverterVersion != batch.ConverterVersion || result.BatchIndex != batch.BatchIndex || result.BatchCount != batch.BatchCount || result.StartLine != batch.StartLine || !equalStringSlices(result.RawRecordIDs, expected.rawRecordIDs) || result.RawImported != 0 || result.RawReplayed != 0 || result.Projected != 0 || result.Existing != 0 || result.Queued != 0 || !isEmptyRawReceipt(result.RawReceipt) {
		return domainmemory.ErrCommonRawUnavailable
	}
	return nil
}

func verifySourceReceipt(receipt domainmemory.CommonRawIntakeReceipt, requestID, ownerID string, input domainmemory.CommonRawIntakeRequest) error {
	expectedManifestID := domainmemory.DeterministicCommonRawManifestID(ownerID, "user:"+ownerID, input.Manifest.SourceType, input.Manifest.SourceIdentity, input.Manifest.ManifestSHA256)
	if receipt.Status != domainmemory.CommonRawStateCompleted || receipt.RequestID != requestID || receipt.ManifestID != expectedManifestID || receipt.ManifestSHA256 != input.Manifest.ManifestSHA256 || receipt.SourceCount != len(input.Records) || receipt.AssetCount != 0 || receipt.Checkpoint != "completed" || receipt.CreatedAt.IsZero() || len(receipt.Records) != len(input.Records) {
		return domainmemory.ErrCommonRawUnavailable
	}
	expected := make(map[string]domainmemory.CommonRawRecord, len(input.Records))
	for _, record := range input.Records {
		expected[record.SourceRecordID] = record
	}
	seenRawIDs := make(map[string]struct{}, len(input.Records))
	for _, stored := range receipt.Records {
		record, ok := expected[stored.SourceRecordID]
		if !ok {
			return domainmemory.ErrCommonRawUnavailable
		}
		expectedRawID := domainmemory.DeterministicCommonRawRecordID(ownerID, "user:"+ownerID, input.Manifest.SourceType, input.Manifest.SourceIdentity, record.SourceRecordID, record.ContentSHA256)
		expectedStorage, expectedObjectRef := commonRawReceiptStorage(record.Content)
		if stored.RawRecordID != expectedRawID || stored.ContentSHA256 != record.ContentSHA256 || stored.ContentSize != int64(len(record.Content)) || stored.StorageKind != expectedStorage || stored.ObjectRef != expectedObjectRef || len(stored.AssetRefs) != 0 {
			return domainmemory.ErrCommonRawUnavailable
		}
		if _, duplicate := seenRawIDs[stored.RawRecordID]; duplicate {
			return domainmemory.ErrCommonRawUnavailable
		}
		seenRawIDs[stored.RawRecordID] = struct{}{}
		delete(expected, stored.SourceRecordID)
	}
	if len(expected) != 0 {
		return domainmemory.ErrCommonRawUnavailable
	}
	return nil
}

func verifyMessageResult(result domainmemory.ChatGPTRawImportResult, requestID, ownerID string, batch domainmemory.ChatGPTRawImportBatch, jobCount int) error {
	expected, err := expectedChatGPTMessageRaw(ownerID, batch)
	if err != nil || result.Validated != len(batch.Records) || result.ExternalManifestSHA256 != batch.ManifestSHA256 || result.ArtifactSHA256 != batch.ArtifactSHA256 || result.ManifestID != expected.manifestID || result.InternalManifestSHA256 != expected.manifestSHA || result.SourceCount != batch.SourceCount || result.SchemaVersion != batch.SchemaVersion || result.ConverterVersion != batch.ConverterVersion || result.BatchIndex != batch.BatchIndex || result.BatchCount != batch.BatchCount || result.StartLine != batch.StartLine || !equalStringSlices(result.RawRecordIDs, expected.rawRecordIDs) || result.RawReceipt.RequestID != requestID || result.RawReceipt.ManifestID != expected.manifestID || result.RawReceipt.Status != domainmemory.CommonRawStateCompleted || result.RawReceipt.ManifestSHA256 != expected.manifestSHA || result.RawReceipt.SourceCount != len(batch.Records) || result.RawReceipt.AssetCount != 0 || result.RawReceipt.Checkpoint != "completed" || result.RawReceipt.CreatedAt.IsZero() || len(result.RawReceipt.Records) != len(batch.Records) {
		return domainmemory.ErrCommonRawUnavailable
	}
	if result.RawImported < 0 || result.RawReplayed < 0 || result.Projected < 0 || result.Existing < 0 || result.Queued < 0 || result.Queued > jobCount || result.Projected+result.Existing != len(batch.Records) {
		return domainmemory.ErrCommonRawUnavailable
	}
	if result.RawReceipt.IdempotentReplay {
		if result.RawImported != 0 || result.RawReplayed != len(batch.Records) {
			return domainmemory.ErrCommonRawUnavailable
		}
	} else if result.RawImported != len(batch.Records) || result.RawReplayed != 0 {
		return domainmemory.ErrCommonRawUnavailable
	}
	seenRawIDs := make(map[string]struct{}, len(batch.Records))
	for _, record := range result.RawReceipt.Records {
		expectedRecord, ok := expected.records[record.SourceRecordID]
		if !ok || record.RawRecordID != expectedRecord.rawRecordID || record.ContentSHA256 != expectedRecord.contentSHA || record.ContentSize != expectedRecord.contentSize || record.StorageKind != expectedRecord.storageKind || record.ObjectRef != expectedRecord.objectRef || len(record.AssetRefs) != 0 {
			return domainmemory.ErrCommonRawUnavailable
		}
		if _, duplicate := seenRawIDs[record.RawRecordID]; duplicate {
			return domainmemory.ErrCommonRawUnavailable
		}
		seenRawIDs[record.RawRecordID] = struct{}{}
	}
	gotReceiptIDs := make([]string, 0, len(result.RawReceipt.Records))
	for rawID := range seenRawIDs {
		gotReceiptIDs = append(gotReceiptIDs, rawID)
	}
	sort.Strings(gotReceiptIDs)
	if !equalStringSlices(gotReceiptIDs, expected.rawRecordIDs) {
		return domainmemory.ErrCommonRawUnavailable
	}
	return nil
}

type expectedMessageRawRecord struct {
	rawRecordID string
	contentSHA  string
	contentSize int64
	storageKind string
	objectRef   string
}

type expectedMessageRawBinding struct {
	manifestID   string
	manifestSHA  string
	rawRecordIDs []string
	records      map[string]expectedMessageRawRecord
}

type chatGPTMessageRawBinding struct {
	Adapter          string `json:"adapter"`
	ManifestSHA256   string `json:"manifest_sha256"`
	ArtifactSHA256   string `json:"artifact_sha256"`
	SourceCount      int    `json:"source_count"`
	SchemaVersion    string `json:"schema_version"`
	ConverterVersion string `json:"converter_version"`
	BatchCount       int    `json:"batch_count"`
}

func expectedChatGPTMessageRaw(ownerID string, batch domainmemory.ChatGPTRawImportBatch) (expectedMessageRawBinding, error) {
	if strings.TrimSpace(ownerID) == "" || len(batch.Records) == 0 {
		return expectedMessageRawBinding{}, domainmemory.ErrCommonRawInvalid
	}
	exportID := strings.TrimSpace(batch.ExportID)
	if exportID == "" {
		exportID = batch.Records[0].ExportID
	}
	binding := chatGPTMessageRawBinding{
		Adapter: chatGPTMessageRawAdapter, ManifestSHA256: batch.ManifestSHA256,
		ArtifactSHA256: batch.ArtifactSHA256, SourceCount: batch.SourceCount,
		SchemaVersion: batch.SchemaVersion, ConverterVersion: batch.ConverterVersion,
		BatchCount: batch.BatchCount,
	}
	provenanceBytes, err := json.Marshal(binding)
	if err != nil {
		return expectedMessageRawBinding{}, err
	}
	provenance := string(provenanceBytes)
	records := make([]domainmemory.CommonRawRecord, 0, len(batch.Records))
	prepared := make(map[string]expectedMessageRawRecord, len(batch.Records))
	for index, item := range batch.Records {
		payload, marshalErr := domainmemory.MarshalChatGPTRawPayload(batch, index)
		if marshalErr != nil {
			return expectedMessageRawBinding{}, marshalErr
		}
		occurredAt := chatGPTImportRawOccurredAt(item)
		hash := domainmemory.SHA256Hex(payload)
		storageKind, objectRef := commonRawReceiptStorage(payload)
		if _, duplicate := prepared[item.EvidenceID]; duplicate {
			return expectedMessageRawBinding{}, domainmemory.ErrCommonRawConflict
		}
		records = append(records, domainmemory.CommonRawRecord{
			SourceRecordID: item.EvidenceID, ParentID: item.ParentNodeID, ThreadID: item.ConversationID,
			Sensitivity: domainmemory.CommonRawPrivateSensitivity, Role: item.Role,
			ContentType: chatGPTMessageRawContentType, OccurredAt: occurredAt, Content: payload,
			ContentSHA256: hash, Provenance: provenance, Rights: "owner", License: "private",
		})
		prepared[item.EvidenceID] = expectedMessageRawRecord{contentSHA: hash, contentSize: int64(len(payload)), storageKind: storageKind, objectRef: objectRef}
	}
	manifest := domainmemory.CommonRawManifest{
		ContractVersion: domainmemory.CommonRawContractVersion, SourceType: chatGPTMessageRawSourceType,
		SourceIdentity: exportID, SourceCount: len(records), AssetCount: 0,
		SchemaVersion: batch.SchemaVersion, ConverterVersion: batch.ConverterVersion,
		Sensitivity: domainmemory.CommonRawPrivateSensitivity, Rights: "owner", License: "private",
		Provenance: provenance, AllowEmpty: false,
	}
	manifestHash, err := domainmemory.CommonRawInputHash(manifest, records, nil)
	if err != nil {
		return expectedMessageRawBinding{}, err
	}
	result := expectedMessageRawBinding{
		manifestSHA: manifestHash,
		manifestID:  domainmemory.DeterministicCommonRawManifestID(ownerID, "user:"+ownerID, manifest.SourceType, manifest.SourceIdentity, manifestHash),
		records:     prepared,
	}
	for sourceID, record := range prepared {
		record.rawRecordID = domainmemory.DeterministicCommonRawRecordID(ownerID, "user:"+ownerID, manifest.SourceType, manifest.SourceIdentity, sourceID, record.contentSHA)
		prepared[sourceID] = record
		result.rawRecordIDs = append(result.rawRecordIDs, record.rawRecordID)
	}
	sort.Strings(result.rawRecordIDs)
	result.records = prepared
	return result, nil
}

func commonRawReceiptStorage(content []byte) (string, string) {
	if len(content) <= domainmemory.CommonRawMaxInlinePayloadSize {
		return domainmemory.CommonRawStorageInline, ""
	}
	hash := domainmemory.SHA256Hex(content)
	return domainmemory.CommonRawStorageObject, "objects/sha256/" + hash[:2] + "/" + hash
}

func chatGPTImportRawOccurredAt(item domainmemory.ChatGPTL3ImportRecord) time.Time {
	for _, value := range []time.Time{item.MessageCreatedAt, item.ConversationCreatedAt, item.ConversationUpdatedAt} {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Unix(0, 0).UTC()
}

func isEmptyRawReceipt(receipt domainmemory.CommonRawIntakeReceipt) bool {
	return receipt.RequestID == "" && receipt.ManifestID == "" && receipt.Status == "" && receipt.ManifestSHA256 == "" && receipt.SourceCount == 0 && receipt.AssetCount == 0 && receipt.Checkpoint == "" && !receipt.IdempotentReplay && len(receipt.Records) == 0 && receipt.CreatedAt.IsZero()
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
