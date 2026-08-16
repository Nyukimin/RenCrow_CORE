package archivesqlite

import (
	"context"
	"database/sql"
	"errors"
)

type archiveParquetSnapshot struct {
	Threads   []threadSummaryParquetRow
	Memory    []l1MemoryEventParquetRow
	News      []l1NewsItemParquetRow
	Knowledge []l1KnowledgeItemParquetRow
	Staging   []l1StagingItemParquetRow
}

func (d *ArchiveSQLiteStore) readArchiveParquetSnapshot(ctx context.Context) (archiveParquetSnapshot, error) {
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return archiveParquetSnapshot{}, err
	}
	snapshot := archiveParquetSnapshot{}
	rollback := func(cause error) (archiveParquetSnapshot, error) {
		return archiveParquetSnapshot{}, rollbackArchiveReadTx(tx, cause)
	}
	if snapshot.Threads, err = readThreadSummaryRows(ctx, tx); err != nil {
		return rollback(err)
	}
	if snapshot.Memory, err = readMemoryArchiveRows(ctx, tx); err != nil {
		return rollback(err)
	}
	if snapshot.News, err = readNewsArchiveRows(ctx, tx); err != nil {
		return rollback(err)
	}
	if snapshot.Knowledge, err = readKnowledgeArchiveRows(ctx, tx); err != nil {
		return rollback(err)
	}
	if snapshot.Staging, err = readStagingArchiveRows(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return archiveParquetSnapshot{}, err
	}
	return snapshot, nil
}

func rollbackArchiveReadTx(tx *sql.Tx, cause error) error {
	if tx == nil {
		return cause
	}
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return errors.Join(cause, err)
	}
	return cause
}

func readThreadSummaryRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}) ([]threadSummaryParquetRow, error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT st.thread_id, st.session_id, st.ts_start, st.ts_end, st.domain, st.summary,
       st.keywords, st.embedding, st.is_novel, st.created_at,
       r.schema_version, r.generation_mode, r.provider, r.failure_code,
       r.evidence_sha256, r.source_turn_count, r.roles_json, r.created_at
FROM session_thread st
LEFT JOIN conversation_thread_summary_receipt r ON r.thread_id = st.thread_id
ORDER BY st.ts_start ASC, st.thread_id ASC`)
	if err != nil {
		return nil, err
	}
	result := make([]threadSummaryParquetRow, 0)
	for rows.Next() {
		var row threadSummaryParquetRow
		var tsEnd sql.NullTime
		var domain sql.NullString
		var isNovel sql.NullBool
		var schemaVersion, generationMode, provider, failureCode, evidenceSHA256, rolesJSON sql.NullString
		var sourceTurnCount sql.NullInt64
		var receiptCreatedAt sql.NullTime
		if err := rows.Scan(
			&row.ThreadID, &row.SessionID, &row.TsStart, &tsEnd, &domain, &row.Summary,
			&row.Keywords, &row.Embedding, &isNovel, &row.CreatedAt,
			&schemaVersion, &generationMode, &provider, &failureCode, &evidenceSHA256,
			&sourceTurnCount, &rolesJSON, &receiptCreatedAt,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if tsEnd.Valid {
			value := tsEnd.Time
			row.TsEnd = &value
		}
		if domain.Valid {
			value := domain.String
			row.Domain = &value
		}
		if isNovel.Valid {
			value := isNovel.Bool
			row.IsNovel = &value
		}
		if schemaVersion.Valid {
			value := schemaVersion.String
			row.SchemaVersion = &value
		}
		if generationMode.Valid {
			value := generationMode.String
			row.GenerationMode = &value
		}
		if provider.Valid {
			value := provider.String
			row.Provider = &value
		}
		if failureCode.Valid {
			value := failureCode.String
			row.FailureCode = &value
		}
		if evidenceSHA256.Valid {
			value := evidenceSHA256.String
			row.EvidenceSHA256 = &value
		}
		if sourceTurnCount.Valid {
			value := sourceTurnCount.Int64
			row.SourceTurnCount = &value
		}
		if rolesJSON.Valid {
			value := rolesJSON.String
			row.RolesJSON = &value
		}
		if receiptCreatedAt.Valid {
			value := receiptCreatedAt.Time
			row.ReceiptCreatedAt = &value
		}
		result = append(result, row)
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil {
		return nil, rowErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return result, nil
}

func readMemoryArchiveRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}) ([]l1MemoryEventParquetRow, error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json, memory_state, layer, source, created_at, updated_at
FROM l1_memory_event_archive ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	result := make([]l1MemoryEventParquetRow, 0)
	for rows.Next() {
		var row l1MemoryEventParquetRow
		if err := rows.Scan(&row.ID, &row.Namespace, &row.SessionID, &row.ThreadID, &row.Speaker, &row.Message, &row.MetaJSON, &row.MemoryState, &row.Layer, &row.Source, &row.CreatedAt, &row.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		result = append(result, row)
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil {
		return nil, rowErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return result, nil
}

func readNewsArchiveRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}) ([]l1NewsItemParquetRow, error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT id, staging_id, category, source_id, source_url, published_at, fetched_at, raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json, created_at, updated_at
FROM l1_news_item_archive ORDER BY COALESCE(published_at, fetched_at) ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	result := make([]l1NewsItemParquetRow, 0)
	for rows.Next() {
		var row l1NewsItemParquetRow
		var published sql.NullTime
		if err := rows.Scan(&row.ID, &row.StagingID, &row.Category, &row.SourceID, &row.SourceURL, &published, &row.FetchedAt, &row.RawText, &row.RawHash, &row.SummaryDraft, &row.KeywordsJSON, &row.LicenseNote, &row.MetaJSON, &row.CreatedAt, &row.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if published.Valid {
			value := published.Time
			row.PublishedAt = &value
		}
		result = append(result, row)
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil {
		return nil, rowErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return result, nil
}

func readKnowledgeArchiveRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}) ([]l1KnowledgeItemParquetRow, error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json, created_at, updated_at
FROM l1_knowledge_item_archive ORDER BY updated_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	result := make([]l1KnowledgeItemParquetRow, 0)
	for rows.Next() {
		var row l1KnowledgeItemParquetRow
		if err := rows.Scan(&row.ID, &row.StagingID, &row.Domain, &row.Title, &row.SourceID, &row.SourceURL, &row.RawText, &row.RawHash, &row.SummaryDraft, &row.KeywordsJSON, &row.LicenseNote, &row.MetaJSON, &row.CreatedAt, &row.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		result = append(result, row)
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil {
		return nil, rowErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return result, nil
}

func readStagingArchiveRows(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}) ([]l1StagingItemParquetRow, error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at, raw_text, raw_hash, summary_draft, keywords_json, license_note, validation_status, meta_json, created_at, updated_at
FROM l1_staging_item_archive ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	result := make([]l1StagingItemParquetRow, 0)
	for rows.Next() {
		var row l1StagingItemParquetRow
		var published sql.NullTime
		if err := rows.Scan(&row.ID, &row.Kind, &row.Namespace, &row.EventID, &row.SourceID, &row.SourceURL, &row.FetchedAt, &published, &row.RawText, &row.RawHash, &row.SummaryDraft, &row.KeywordsJSON, &row.LicenseNote, &row.ValidationStatus, &row.MetaJSON, &row.CreatedAt, &row.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if published.Valid {
			value := published.Time
			row.PublishedAt = &value
		}
		result = append(result, row)
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil {
		return nil, rowErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return result, nil
}
