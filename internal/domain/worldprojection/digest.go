package worldprojection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// PayloadDigest is the projection digest WORLD records for a payload. It is
// SHA-256 over deterministic JSON with sorted object keys; it is not the hash
// of the exact wire bytes. CORE must use this same function for its outbox
// record so an accepted head can be compared to the submitted revision.
func PayloadDigest(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", fmt.Errorf("%w: payload is not valid JSON", ErrInvalidProjection)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: payload has trailing data", ErrInvalidProjection)
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, value); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// WorkSetContentDigest identifies the active work content independent of the
// delivery revision and validity horizon. A projector can compare it with the
// last accepted snapshot and avoid spending a new source revision when nothing
// changed.
func WorkSetContentDigest(workSet WorkSet) (string, error) {
	content := struct {
		ContractVersion string     `json:"contract_version"`
		WorldID         string     `json:"world_id"`
		ActorID         string     `json:"actor_id"`
		SourceOwner     string     `json:"source_owner"`
		SourceEpoch     string     `json:"source_epoch"`
		ActiveWork      []WorkItem `json:"active_work"`
	}{
		ContractVersion: workSet.ContractVersion,
		WorldID:         workSet.WorldID,
		ActorID:         workSet.ActorID,
		SourceOwner:     workSet.SourceOwner,
		SourceEpoch:     workSet.SourceEpoch,
		ActiveWork:      workSet.ActiveWork,
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("%w: work set content is not encodable", ErrInvalidProjection)
	}
	return PayloadDigest(raw)
}

// LifePolicyContentDigest identifies the permission content independent of the
// delivery revision and validity horizon. A projector can extend validity
// without spending a new source revision when the permissions are unchanged.
func LifePolicyContentDigest(policy LifePolicy) (string, error) {
	content := struct {
		ContractVersion                string   `json:"contract_version"`
		WorldID                        string   `json:"world_id"`
		ActorID                        string   `json:"actor_id"`
		SourceOwner                    string   `json:"source_owner"`
		SourceEpoch                    string   `json:"source_epoch"`
		PersonaRevision                string   `json:"persona_revision"`
		ConditionProfileRef            string   `json:"condition_profile_ref"`
		AllowedNonCognitiveActivityIDs []string `json:"allowed_non_cognitive_activity_ids"`
		ExpiredFallbackActivityIDs     []string `json:"expired_fallback_activity_ids"`
	}{
		ContractVersion:                policy.ContractVersion,
		WorldID:                        policy.WorldID,
		ActorID:                        policy.ActorID,
		SourceOwner:                    policy.SourceOwner,
		SourceEpoch:                    policy.SourceEpoch,
		PersonaRevision:                policy.PersonaRevision,
		ConditionProfileRef:            policy.ConditionProfileRef,
		AllowedNonCognitiveActivityIDs: policy.AllowedNonCognitiveActivityIDs,
		ExpiredFallbackActivityIDs:     policy.ExpiredFallbackActivityIDs,
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("%w: life policy content is not encodable", ErrInvalidProjection)
	}
	return PayloadDigest(raw)
}

func writeCanonical(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(raw)
	case json.Number:
		// The accepted projection schemas contain integer revisions only.
		if _, err := strconv.ParseUint(v.String(), 10, 64); err != nil {
			return fmt.Errorf("%w: non-integer JSON number", ErrInvalidProjection)
		}
		buf.WriteString(v.String())
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			raw, err := json.Marshal(key)
			if err != nil {
				return err
			}
			buf.Write(raw)
			buf.WriteByte(':')
			if err := writeCanonical(buf, v[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("%w: unsupported JSON value", ErrInvalidProjection)
	}
	return nil
}
