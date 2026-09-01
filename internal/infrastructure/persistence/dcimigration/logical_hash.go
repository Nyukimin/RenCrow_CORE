package dcimigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// The logical hash intentionally does not read SQLite pages.  A source may be
// copied, vacuumed, or created with another page size without changing its
// logical hash.  The row digest budget is global to one content pass, so the
// largest retained digest set is bounded to 32 MiB.
const (
	maxLogicalTables   = 16384
	maxLogicalColumns  = 512
	maxLogicalRows     = 1_000_000
	maxLogicalCellSize = 4 << 20
	maxLogicalRowSize  = 16 << 20
)

type logicalHashes struct {
	Full   string
	Schema string
	NonDCI string
}

type logicalRowExcluder func(table string, columns []string, values []any) (bool, error)

type logicalTable struct {
	Name    string
	Columns []logicalColumn
}

type logicalColumn struct {
	CID     int64
	Name    string
	Type    string
	NotNull int64
	Default any
	Primary int64
	Hidden  int64
}

func hashSQLiteLogical(ctx context.Context, db *sql.DB, exclude logicalRowExcluder) (logicalHashes, error) {
	if err := ctx.Err(); err != nil {
		return logicalHashes{}, err
	}
	tables, schemaHash, err := hashSQLiteSchema(ctx, db)
	if err != nil {
		return logicalHashes{}, err
	}
	contentHash, err := hashSQLiteContent(ctx, db, tables, nil)
	if err != nil {
		return logicalHashes{}, err
	}
	full := combineLogicalHash(schemaHash, contentHash)
	nonDCI := full
	if exclude != nil {
		nonDCIContent, err := hashSQLiteContent(ctx, db, tables, exclude)
		if err != nil {
			return logicalHashes{}, err
		}
		nonDCI = combineLogicalHash(schemaHash, nonDCIContent)
	}
	return logicalHashes{Full: full, Schema: schemaHash, NonDCI: nonDCI}, nil
}

func hashSQLiteSchema(ctx context.Context, db *sql.DB) ([]logicalTable, string, error) {
	var userVersion, applicationID int64
	var encoding string
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return nil, "", newCodedError("logical_hash_schema", "read SQLite user_version: %w", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return nil, "", newCodedError("logical_hash_schema", "read SQLite application_id: %w", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA encoding").Scan(&encoding); err != nil {
		return nil, "", newCodedError("logical_hash_schema", "read SQLite encoding: %w", err)
	}

	h := sha256.New()
	if err := writeHashDomain(h, "schema"); err != nil {
		return nil, "", err
	}
	if err := writeHashField(h, "header", []any{userVersion, applicationID, encoding}); err != nil {
		return nil, "", newCodedError("logical_hash_schema", "encode SQLite header: %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, sql FROM sqlite_schema ORDER BY type, name, tbl_name, COALESCE(sql, '')`)
	if err != nil {
		return nil, "", newCodedError("logical_hash_schema", "read sqlite_schema: %w", err)
	}
	schemaObjects := 0
	if err := writeHashField(h, "sqlite_schema", []any{int64(0)}); err != nil {
		_ = rows.Close()
		return nil, "", newCodedError("logical_hash_schema", "encode sqlite_schema header: %v", err)
	}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return nil, "", err
		}
		values := make([]any, 4)
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			_ = rows.Close()
			return nil, "", newCodedError("logical_hash_schema", "scan sqlite_schema: %v", err)
		}
		schemaObjects++
		if schemaObjects > maxLogicalRows {
			_ = rows.Close()
			return nil, "", newCodedError("logical_hash_bound", "SQLite schema object count exceeds the bound")
		}
		if err := writeHashField(h, "object", values); err != nil {
			_ = rows.Close()
			return nil, "", newCodedError("logical_hash_schema", "encode sqlite_schema object: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, "", newCodedError("logical_hash_schema", "iterate sqlite_schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, "", newCodedError("logical_hash_schema", "close sqlite_schema: %v", err)
	}

	tables, err := logicalTables(ctx, db)
	if err != nil {
		return nil, "", err
	}
	if err := writeHashField(h, "table_count", []any{int64(len(tables))}); err != nil {
		return nil, "", newCodedError("logical_hash_schema", "encode SQLite table count: %v", err)
	}
	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if err := writeHashField(h, "table", []any{table.Name}); err != nil {
			return nil, "", newCodedError("logical_hash_schema", "encode SQLite table name: %v", err)
		}
		if err := writeHashField(h, "column_count", []any{int64(len(table.Columns))}); err != nil {
			return nil, "", newCodedError("logical_hash_schema", "encode SQLite column count: %v", err)
		}
		for _, column := range table.Columns {
			if err := writeHashField(h, "column", []any{column.CID, column.Name, column.Type, column.NotNull, column.Default, column.Primary, column.Hidden}); err != nil {
				return nil, "", newCodedError("logical_hash_schema", "encode SQLite table descriptor: %v", err)
			}
		}
	}
	return tables, hex.EncodeToString(h.Sum(nil)), nil
}

func logicalTables(ctx context.Context, db *sql.DB) ([]logicalTable, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, newCodedError("logical_hash_schema", "list SQLite tables: %w", err)
	}
	names := make([]string, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, newCodedError("logical_hash_schema", "scan SQLite table name: %v", err)
		}
		if name == "" {
			_ = rows.Close()
			return nil, newCodedError("logical_hash_schema", "SQLite table name is empty")
		}
		if _, exists := seen[name]; exists {
			_ = rows.Close()
			return nil, newCodedError("logical_hash_schema", "SQLite table name is duplicated")
		}
		seen[name] = struct{}{}
		if len(names) >= maxLogicalTables {
			_ = rows.Close()
			return nil, newCodedError("logical_hash_bound", "SQLite table count exceeds the bound")
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, newCodedError("logical_hash_schema", "iterate SQLite table names: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, newCodedError("logical_hash_schema", "close SQLite table names: %v", err)
	}
	tables := make([]logicalTable, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		columns, err := logicalColumns(ctx, db, name)
		if err != nil {
			return nil, err
		}
		tables = append(tables, logicalTable{Name: name, Columns: columns})
	}
	return tables, nil
}

func logicalColumns(ctx context.Context, db *sql.DB, table string) ([]logicalColumn, error) {
	query := "PRAGMA table_xinfo(" + quoteSQLiteString(table) + ")"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, newCodedError("logical_hash_schema", "read SQLite table_xinfo: %w", err)
	}
	defer rows.Close()
	columns := make([]logicalColumn, 0)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var column logicalColumn
		if err := rows.Scan(&column.CID, &column.Name, &column.Type, &column.NotNull, &column.Default, &column.Primary, &column.Hidden); err != nil {
			return nil, newCodedError("logical_hash_schema", "scan SQLite table_xinfo: %v", err)
		}
		if column.CID != int64(len(columns)) || column.Name == "" {
			return nil, newCodedError("logical_hash_schema", "SQLite table_xinfo descriptor is not canonical")
		}
		if len(columns) >= maxLogicalColumns {
			return nil, newCodedError("logical_hash_bound", "SQLite column count exceeds the bound")
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, newCodedError("logical_hash_schema", "iterate SQLite table_xinfo: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, newCodedError("logical_hash_schema", "close SQLite table_xinfo: %v", err)
	}
	if len(columns) == 0 {
		return nil, newCodedError("logical_hash_schema", "SQLite table has no columns")
	}
	return columns, nil
}

func hashSQLiteContent(ctx context.Context, db *sql.DB, tables []logicalTable, exclude logicalRowExcluder) (string, error) {
	h := sha256.New()
	if err := writeHashDomain(h, "content"); err != nil {
		return "", err
	}
	if err := writeHashField(h, "table_count", []any{int64(len(tables))}); err != nil {
		return "", newCodedError("logical_hash_content", "encode SQLite table count: %v", err)
	}
	for _, table := range tables {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		digests, err := hashSQLiteTableRows(ctx, db, table, exclude, maxLogicalRows)
		if err != nil {
			return "", err
		}
		tableHash := sha256.New()
		if err := writeHashDomain(tableHash, "table"); err != nil {
			return "", err
		}
		if err := writeHashField(tableHash, "name", []any{table.Name}); err != nil {
			return "", newCodedError("logical_hash_content", "encode SQLite table name: %v", err)
		}
		if err := writeHashField(tableHash, "row_count", []any{int64(len(digests))}); err != nil {
			return "", newCodedError("logical_hash_content", "encode SQLite row count: %v", err)
		}
		for _, digest := range digests {
			if err := writeLengthPrefixed(tableHash, digest[:]); err != nil {
				return "", newCodedError("logical_hash_content", "encode SQLite row digest: %v", err)
			}
		}
		if err := writeHashField(h, "table_hash", []any{table.Name, hex.EncodeToString(tableHash.Sum(nil))}); err != nil {
			return "", newCodedError("logical_hash_content", "encode SQLite table hash: %v", err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashSQLiteTableRows(ctx context.Context, db *sql.DB, table logicalTable, exclude logicalRowExcluder, retainedRowLimit int) ([][32]byte, error) {
	if retainedRowLimit <= 0 {
		return nil, newCodedError("logical_hash_bound", "SQLite retained row limit must be positive")
	}
	columns := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		columns[index] = column.Name
	}
	selectedColumns := make([]string, len(columns))
	for index, column := range columns {
		quoted := quoteSQLiteIdentifier(column)
		// Unary plus makes this a typeless expression.  That preserves the
		// SQLite storage class/value instead of allowing modernc.org/sqlite to
		// convert declared DATE/TIME columns to time.Time.
		selectedColumns[index] = "+" + quoted + " AS " + quoted
	}
	query := "SELECT " + strings.Join(selectedColumns, ", ") + " FROM " + quoteSQLiteIdentifier(table.Name)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, newCodedError("logical_hash_content", "read SQLite table rows: %w", err)
	}
	defer rows.Close()
	actualColumns, err := rows.Columns()
	if err != nil {
		return nil, newCodedError("logical_hash_content", "read SQLite row columns: %v", err)
	}
	if len(actualColumns) != len(columns) {
		return nil, newCodedError("logical_hash_schema", "SQLite row columns do not match table_xinfo")
	}
	for index, actual := range actualColumns {
		if actual != columns[index] {
			return nil, newCodedError("logical_hash_schema", "SQLite row column %d is %q, want %q", index, actual, columns[index])
		}
	}
	digests := make([][32]byte, 0)
	scannedRows := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if scannedRows >= maxLogicalRows {
			return nil, newCodedError("logical_hash_bound", "SQLite scanned row count exceeds the bound")
		}
		scannedRows++
		values := make([]any, len(columns))
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, newCodedError("logical_hash_content", "scan SQLite row: %v", err)
		}
		excluded := false
		if exclude != nil {
			excluded, err = exclude(table.Name, columns, values)
			if err != nil {
				return nil, err
			}
		}
		if excluded {
			continue
		}
		if len(digests) >= retainedRowLimit {
			return nil, newCodedError("logical_hash_bound", "SQLite retained row count exceeds the bound")
		}
		digest, err := typedRowDigest(values)
		if err != nil {
			return nil, newCodedError("logical_hash_content", "encode SQLite row: %v", err)
		}
		digests = append(digests, digest)
	}
	if err := rows.Err(); err != nil {
		return nil, newCodedError("logical_hash_content", "iterate SQLite table rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, newCodedError("logical_hash_content", "close SQLite table rows: %v", err)
	}
	sort.Slice(digests, func(left, right int) bool {
		return bytes.Compare(digests[left][:], digests[right][:]) < 0
	})
	return digests, nil
}

func typedRowDigest(values []any) ([32]byte, error) {
	if len(values) > maxLogicalColumns {
		return [32]byte{}, fmt.Errorf("SQLite row column count exceeds the bound")
	}
	h := sha256.New()
	if err := writeHashDomain(h, "row"); err != nil {
		return [32]byte{}, err
	}
	if err := writeHashField(h, "column_count", []any{int64(len(values))}); err != nil {
		return [32]byte{}, err
	}
	rowBytes := 0
	for _, value := range values {
		written, err := writeTypedValue(h, value, &rowBytes)
		if err != nil {
			return [32]byte{}, err
		}
		if written > maxLogicalRowSize || rowBytes > maxLogicalRowSize {
			return [32]byte{}, fmt.Errorf("SQLite row exceeds the size bound")
		}
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

func writeHashDomain(w io.Writer, domain string) error {
	if err := writeLengthPrefixed(w, []byte(LogicalHashAlgorithm)); err != nil {
		return err
	}
	return writeLengthPrefixed(w, []byte(domain))
}

func writeHashField(w io.Writer, name string, values []any) error {
	if err := writeLengthPrefixed(w, []byte(name)); err != nil {
		return err
	}
	if len(values) > maxLogicalColumns {
		return fmt.Errorf("SQLite hash field column count exceeds the bound")
	}
	if err := writeUint64(w, uint64(len(values))); err != nil {
		return err
	}
	rowBytes := 0
	for _, value := range values {
		if _, err := writeTypedValue(w, value, &rowBytes); err != nil {
			return err
		}
	}
	return nil
}

func writeTypedValue(w io.Writer, value any, rowBytes *int) (int, error) {
	tag := byte(0)
	var data []byte
	switch typed := value.(type) {
	case nil:
		// SQLite NULL has no payload but remains distinct from every other type.
	case int64:
		tag = 'i'
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(typed))
		data = encoded[:]
	case float64:
		tag = 'f'
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], math.Float64bits(typed))
		data = encoded[:]
	case string:
		tag = 's'
		data = []byte(typed)
	case []byte:
		tag = 'b'
		data = typed
	case sql.RawBytes:
		tag = 'b'
		data = []byte(typed)
	default:
		return 0, fmt.Errorf("unsupported SQLite value type %T", value)
	}
	if len(data) > maxLogicalCellSize {
		return 0, fmt.Errorf("SQLite cell exceeds the size bound")
	}
	encodedBytes := 1 + 8 + len(data)
	if *rowBytes > maxLogicalRowSize-encodedBytes {
		return 0, fmt.Errorf("SQLite row exceeds the size bound")
	}
	if _, err := w.Write([]byte{tag}); err != nil {
		return 0, err
	}
	if err := writeLengthPrefixed(w, data); err != nil {
		return 0, err
	}
	*rowBytes += encodedBytes
	return encodedBytes, nil
}

func writeLengthPrefixed(w io.Writer, data []byte) error {
	if err := writeUint64(w, uint64(len(data))); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	_, err := w.Write(data)
	return err
}

func writeUint64(w io.Writer, value uint64) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, err := w.Write(encoded[:])
	return err
}

func combineLogicalHash(schemaHash, contentHash string) string {
	h := sha256.New()
	_ = writeHashDomain(h, "database")
	_ = writeHashField(h, "schema", []any{schemaHash})
	_ = writeHashField(h, "content", []any{contentHash})
	return hex.EncodeToString(h.Sum(nil))
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteSQLiteString(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
