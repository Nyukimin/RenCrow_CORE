package categoryrecall

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func openReadOnlySQLite(path string) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errUnavailable("database path is not configured")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, errUnavailable("database path unavailable: " + err.Error())
	}
	if info.IsDir() {
		return nil, errUnavailable("database path is a directory")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, errUnavailable("database path invalid: " + err.Error())
	}
	dsn := "file:" + absPath + "?mode=ro&_pragma=busy_timeout(5000)&_time_format=sqlite"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errUnavailable("open read-only sqlite: " + err.Error())
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, errUnavailable("ping read-only sqlite: " + err.Error())
	}
	return db, nil
}

func tableExists(db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count)
	return count > 0, err
}

func tableColumnExists(db *sql.DB, table string, column string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue interface{}
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(column)) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func boundedLimit(limit int) int {
	if limit <= 0 {
		return 3
	}
	if limit > 24 {
		return 24
	}
	return limit
}

func errUnavailable(reason string) error {
	return fmt.Errorf("category source unavailable: %s", strings.TrimSpace(reason))
}

func normalizeCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "movies", "film", "films":
		return "movie"
	case "television", "tv":
		return "drama"
	case "people", "human":
		return "person"
	case "hobbies":
		return "hobby"
	case "books":
		return "book"
	case "games":
		return "game"
	case "news_articles":
		return "news"
	case "investments", "finance":
		return "investment"
	default:
		return value
	}
}

func metadataString(meta map[string]interface{}, key string) string {
	if meta == nil {
		return ""
	}
	value, ok := meta[key]
	if !ok {
		return ""
	}
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func metadataTime(meta map[string]interface{}, key string) time.Time {
	value := metadataString(meta, key)
	return parseSourceTime(value)
}

func parseSourceTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func lexicalPredicate(message string, fields ...string) (string, []interface{}) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "1 = 1", nil
	}
	clauses := make([]string, 0, len(fields))
	args := make([]interface{}, 0, len(fields)*2)
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		field = "COALESCE(" + field + ", '')"
		clauses = append(clauses, "(TRIM("+field+") <> '' AND (instr(lower(?), lower("+field+")) > 0 OR instr(lower("+field+"), lower(?)) > 0))")
		args = append(args, message, message)
	}
	if len(clauses) == 0 {
		return "1 = 1", nil
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func queryMatches(message string, fields ...string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" {
		return true
	}
	terms := strings.Fields(message)
	for _, field := range fields {
		field = strings.ToLower(strings.TrimSpace(field))
		if field == "" {
			continue
		}
		if strings.Contains(message, field) || strings.Contains(field, message) {
			return true
		}
		for _, term := range terms {
			term = strings.TrimSpace(term)
			if len([]rune(term)) > 1 && strings.Contains(field, term) {
				return true
			}
		}
	}
	return false
}

func decodeJSONMap(value string) map[string]interface{} {
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return nil
	}
	return out
}
