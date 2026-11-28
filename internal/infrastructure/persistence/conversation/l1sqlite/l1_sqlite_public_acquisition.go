package l1sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const L1PublicAcquisitionLeaseTTL = 10 * time.Minute

type L1PublicAcquisitionLease struct {
	CanonicalURL string
	Token        string
	ExpiresAt    time.Time
}

type PublicAcquisitionLeaseStore interface {
	ClaimPublicAcquisition(context.Context, string, time.Time) (L1PublicAcquisitionLease, bool, error)
	ReleasePublicAcquisition(context.Context, L1PublicAcquisitionLease, time.Time) (bool, error)
	CompletePublicSourceAcquisition(context.Context, L1PublicAcquisitionLease, string, time.Time, string, string) (bool, error)
}

// CanonicalPublicAcquisitionURL establishes the shared identity used by every
// public acquisition caller. Query parameters retain their original meaning;
// only URL syntax that does not select a resource is removed or normalized.
func CanonicalPublicAcquisitionURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Opaque != "" || u.User != nil || u.Hostname() == "" {
		return "", errors.New("invalid public acquisition URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("public acquisition URL must use HTTP or HTTPS")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	u.Host = host
	u.Fragment = ""
	u.RawFragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func (s *L1SQLiteStore) ClaimPublicAcquisition(ctx context.Context, rawURL string, now time.Time) (L1PublicAcquisitionLease, bool, error) {
	if s == nil || s.db == nil || ctx == nil {
		return L1PublicAcquisitionLease{}, false, errors.New("l1 public acquisition store and context are required")
	}
	canonicalURL, err := CanonicalPublicAcquisitionURL(rawURL)
	if err != nil {
		return L1PublicAcquisitionLease{}, false, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	expires := now.Add(L1PublicAcquisitionLeaseTTL)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM l1_public_acquisition_lease WHERE lease_until<=? AND canonical_url<>?`, now, canonicalURL); err != nil {
		return L1PublicAcquisitionLease{}, false, fmt.Errorf("reap expired l1 public acquisition leases: %w", err)
	}
	var lease L1PublicAcquisitionLease
	lease.CanonicalURL = canonicalURL
	err = s.db.QueryRowContext(ctx, `
INSERT INTO l1_public_acquisition_lease(canonical_url, lease_token, lease_until, updated_at)
VALUES(?, lower(hex(randomblob(32))), ?, ?)
ON CONFLICT(canonical_url) DO UPDATE SET
	lease_token=lower(hex(randomblob(32))), lease_until=excluded.lease_until, updated_at=excluded.updated_at
WHERE l1_public_acquisition_lease.lease_until <= excluded.updated_at
RETURNING lease_token, lease_until
`, canonicalURL, expires, now).Scan(&lease.Token, &lease.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return L1PublicAcquisitionLease{}, false, nil
	}
	if err != nil {
		return L1PublicAcquisitionLease{}, false, fmt.Errorf("claim l1 public acquisition lease: %w", err)
	}
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	return lease, true, nil
}

// ReleasePublicAcquisition is fenced by the opaque token and the live expiry.
// An expired worker cannot clear or complete a newer worker's lease.
func (s *L1SQLiteStore) ReleasePublicAcquisition(ctx context.Context, lease L1PublicAcquisitionLease, now time.Time) (bool, error) {
	if s == nil || s.db == nil || ctx == nil {
		return false, errors.New("l1 public acquisition store and context are required")
	}
	canonicalURL, err := CanonicalPublicAcquisitionURL(lease.CanonicalURL)
	if err != nil || canonicalURL != lease.CanonicalURL || strings.TrimSpace(lease.Token) == "" {
		return false, errors.New("l1 public acquisition lease identity is invalid")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
DELETE FROM l1_public_acquisition_lease
WHERE canonical_url=? AND lease_token=? AND lease_until>?
`, canonicalURL, lease.Token, now.UTC())
	if err != nil {
		return false, fmt.Errorf("release l1 public acquisition lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect l1 public acquisition lease release: %w", err)
	}
	return rows == 1, nil
}

// CompletePublicSourceAcquisition atomically stamps the exact registered
// source and consumes its live lease. A stale worker cannot write a late
// status after another acquisition has reclaimed the URL.
func (s *L1SQLiteStore) CompletePublicSourceAcquisition(ctx context.Context, lease L1PublicAcquisitionLease, sourceID string, fetchedAt time.Time, status, lastError string) (bool, error) {
	if s == nil || s.db == nil || ctx == nil {
		return false, errors.New("l1 public acquisition store and context are required")
	}
	canonicalURL, err := CanonicalPublicAcquisitionURL(lease.CanonicalURL)
	if err != nil || canonicalURL != lease.CanonicalURL || strings.TrimSpace(lease.Token) == "" || strings.TrimSpace(sourceID) == "" {
		return false, errors.New("l1 public source completion identity is invalid")
	}
	status = strings.TrimSpace(status)
	lastError = strings.TrimSpace(lastError)
	if err := validateL1SourceFetchStatus(status); err != nil {
		return false, err
	}
	if status == L1SourceFetchStatusError && lastError == "" {
		return false, errors.New("l1 public source completion error requires a reason")
	}
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin l1 public source completion: %w", err)
	}
	defer tx.Rollback()
	var ownedURL string
	err = tx.QueryRowContext(ctx, `
DELETE FROM l1_public_acquisition_lease
WHERE canonical_url=? AND lease_token=? AND lease_until>?
RETURNING canonical_url
`, canonicalURL, lease.Token, now).Scan(&ownedURL)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("consume l1 public source lease: %w", err)
	}
	var registeredURL string
	var enabled int
	err = tx.QueryRowContext(ctx, `SELECT url, enabled FROM l1_source_registry WHERE source_id=?`, strings.TrimSpace(sourceID)).Scan(&registeredURL, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read l1 source registry for acquisition completion: %w", err)
	}
	registeredCanonical, canonicalErr := CanonicalPublicAcquisitionURL(registeredURL)
	if canonicalErr != nil || registeredCanonical != ownedURL || enabled != 1 {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `
UPDATE l1_source_registry
SET last_fetched_at=?, last_status=?, last_error=?, updated_at=?
WHERE source_id=? AND url=? AND enabled=1
`, fetchedAt.UTC(), status, lastError, now, strings.TrimSpace(sourceID), registeredURL)
	if err != nil {
		return false, fmt.Errorf("stamp l1 source registry acquisition: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect l1 source registry acquisition stamp: %w", err)
	}
	if rows != 1 {
		return false, nil
	}
	namespace, err := BuildL1Namespace(NamespaceKindKnowledge, "source_registry")
	if err != nil {
		return false, err
	}
	payload := map[string]interface{}{
		"source_id": strings.TrimSpace(sourceID), "status": status, "last_error": lastError,
		"fetched_at": fetchedAt.UTC().Format(time.RFC3339),
	}
	if _, err := appendL1EventLog(ctx, tx, "source_registry.fetched", namespace, "", "", 0, "", payload, "source_registry"); err != nil {
		return false, fmt.Errorf("append l1 source registry acquisition event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit l1 public source acquisition: %w", err)
	}
	return true, nil
}
