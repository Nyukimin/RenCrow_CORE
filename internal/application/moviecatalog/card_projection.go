package moviecatalog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Card is the public D0/D1 projection item consumed by Viewer handlers.  The
// depth and root path are derived at read time; neither is stored in the
// movie catalog tables.
type Card struct {
	Kind            string   `json:"kind"`
	TargetID        string   `json:"target_id"`
	TargetLabel     string   `json:"target_label"`
	TargetURL       string   `json:"target_url"`
	Depth           int      `json:"depth"`
	RootIDs         []string `json:"root_ids"`
	RelationType    string   `json:"relation_type"`
	RelationSource  string   `json:"relation_source"`
	ValidationState string   `json:"validation_state"`
	ProvenanceURLs  []string `json:"provenance_urls"`
	Familiarity     string   `json:"familiarity,omitempty"`
	Sentiment       string   `json:"sentiment,omitempty"`
}

// These aliases make the item kinds explicit for handlers and downstream
// clients while retaining one deterministic projection implementation.
type MovieCard = Card
type PersonCard = Card
type MusicCard = Card
type SourceWorkCard = Card

// Cards returns the total number of derived cards and one deterministic page.
// limit <= 0 means all cards.  D1 edges are read only from D0 roots; cards
// discovered at depth 1 are never used as new roots.
func Cards(db *sql.DB, limit int, offset int) (int, []Card, error) {
	if db == nil {
		return 0, nil, fmt.Errorf("movie catalog database is nil")
	}
	roots, err := SelectRoots(db, 0)
	if err != nil {
		return 0, nil, err
	}
	projected := map[string]*Card{}
	for _, root := range roots {
		addProjectedCard(projected, Card{
			Kind:            root.Kind,
			TargetID:        root.TargetID,
			TargetLabel:     root.TargetLabel,
			TargetURL:       root.TargetURL,
			Depth:           0,
			RootIDs:         append([]string(nil), root.RootIDs...),
			RelationSource:  root.Source,
			ValidationState: root.ValidationState,
			ProvenanceURLs:  append([]string(nil), root.ProvenanceURLs...),
			Familiarity:     root.Familiarity,
			Sentiment:       root.Sentiment,
		})
		if err := projectDirectMoviePeople(db, root, projected); err != nil {
			return 0, nil, err
		}
		if root.Kind == "movie" && tableExists(db, "movie_related_credits") {
			if err := projectDirectCredits(db, root, projected); err != nil {
				return 0, nil, err
			}
		}
	}

	cards := make([]Card, 0, len(projected))
	for _, card := range projected {
		card.RootIDs = sortedUniqueStrings(card.RootIDs)
		card.ProvenanceURLs = sortedUniqueStrings(card.ProvenanceURLs)
		cards = append(cards, *card)
	}
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].Depth != cards[j].Depth {
			return cards[i].Depth < cards[j].Depth
		}
		if cards[i].Kind != cards[j].Kind {
			return cards[i].Kind < cards[j].Kind
		}
		if cards[i].TargetLabel != cards[j].TargetLabel {
			return cards[i].TargetLabel < cards[j].TargetLabel
		}
		if cards[i].TargetID != cards[j].TargetID {
			return cards[i].TargetID < cards[j].TargetID
		}
		return cards[i].RelationType < cards[j].RelationType
	})
	total := len(cards)
	if offset < 0 {
		offset = 0
	}
	if offset >= len(cards) {
		return total, []Card{}, nil
	}
	if limit <= 0 || offset+limit > len(cards) {
		limit = len(cards) - offset
	}
	return total, cards[offset : offset+limit], nil
}

// ProjectCards is a descriptive alias for callers that want to distinguish
// this read-only derived view from the catalog's stored item lists.
func ProjectCards(db *sql.DB, limit int, offset int) (int, []Card, error) {
	return Cards(db, limit, offset)
}

func addProjectedCard(projected map[string]*Card, incoming Card) {
	incoming.Kind = strings.ToLower(strings.TrimSpace(incoming.Kind))
	incoming.TargetID = strings.TrimSpace(incoming.TargetID)
	incoming.TargetLabel = strings.TrimSpace(incoming.TargetLabel)
	incoming.TargetURL = strings.TrimSpace(incoming.TargetURL)
	incoming.ValidationState = strings.ToLower(strings.TrimSpace(incoming.ValidationState))
	if incoming.ValidationState == "" {
		incoming.ValidationState = "validated"
	}
	incoming.RootIDs = sortedUniqueStrings(incoming.RootIDs)
	incoming.ProvenanceURLs = sortedUniqueStrings(incoming.ProvenanceURLs)
	key := cardProjectionKey(incoming)
	if previous, ok := projected[key]; ok {
		if incoming.Depth < previous.Depth {
			// Preserve the lowest depth, but do not lose roots and provenance
			// collected through another path.
			incoming.RootIDs = sortedUniqueStrings(append(incoming.RootIDs, previous.RootIDs...))
			incoming.ProvenanceURLs = sortedUniqueStrings(append(incoming.ProvenanceURLs, previous.ProvenanceURLs...))
			*previous = incoming
			return
		}
		previous.RootIDs = sortedUniqueStrings(append(previous.RootIDs, incoming.RootIDs...))
		previous.ProvenanceURLs = sortedUniqueStrings(append(previous.ProvenanceURLs, incoming.ProvenanceURLs...))
		if previous.TargetLabel == "" {
			previous.TargetLabel = incoming.TargetLabel
		}
		if previous.TargetURL == "" {
			previous.TargetURL = incoming.TargetURL
		}
		if previous.RelationType == "" || incoming.RelationType < previous.RelationType {
			previous.RelationType = incoming.RelationType
			previous.RelationSource = incoming.RelationSource
		}
		if projectionStateRank(incoming.ValidationState) > projectionStateRank(previous.ValidationState) {
			previous.ValidationState = incoming.ValidationState
		}
		if previous.Familiarity == "" {
			previous.Familiarity = incoming.Familiarity
		}
		if previous.Sentiment == "" {
			previous.Sentiment = incoming.Sentiment
		}
		return
	}
	projected[key] = &incoming
}

func projectionStateRank(state string) int {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "validated", "confirmed", "ready", "active":
		return 3
	case "partial":
		return 2
	case "unresolved":
		return 1
	default:
		return 0
	}
}

func cardProjectionKey(card Card) string {
	if strings.TrimSpace(card.TargetID) != "" {
		return card.Kind + "\x00" + card.TargetID
	}
	return card.Kind + "\x00partial\x00" + normalizeCardText(card.TargetLabel) + "\x00" + normalizeCardText(card.RelationType) + "\x00" + strings.Join(sortedUniqueStrings(card.ProvenanceURLs), "\x00")
}

func projectDirectMoviePeople(db *sql.DB, root CardRoot, projected map[string]*Card) error {
	if !tableExists(db, "movie_people") {
		return nil
	}
	if root.Kind == "movie" {
		rows, err := db.Query(`
SELECT mp.person_id,
       COALESCE(NULLIF(mp.person_name,''), NULLIF(p.name,''), ''),
       COALESCE(NULLIF(mp.person_url,''), NULLIF(p.url,''), ''),
       COALESCE(mp.role,''), COALESCE(mp.source,''),
	       CASE WHEN p.person_id IS NULL OR COALESCE(p.url,'')='' THEN 'partial' ELSE 'validated' END
FROM movie_people mp
LEFT JOIN people p ON p.person_id=mp.person_id
WHERE mp.movie_id=?`, root.TargetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var targetID, label, targetURL, relation, source, state string
			if err := rows.Scan(&targetID, &label, &targetURL, &relation, &source, &state); err != nil {
				return err
			}
			if strings.TrimSpace(targetID) == "" && strings.TrimSpace(label) == "" && strings.TrimSpace(targetURL) == "" {
				continue
			}
			addProjectedCard(projected, Card{Kind: "person", TargetID: targetID, TargetLabel: label, TargetURL: targetURL, Depth: 1, RootIDs: root.RootIDs, RelationType: relation, RelationSource: source, ValidationState: state, ProvenanceURLs: []string{targetURL, root.TargetURL}})
		}
		return rows.Err()
	}
	if root.Kind != "person" {
		return nil
	}
	rows, err := db.Query(`
SELECT mp.movie_id,
       COALESCE(NULLIF(mp.movie_title,''), NULLIF(m.title,''), ''),
       COALESCE(NULLIF(mp.movie_url,''), NULLIF(m.url,''), ''),
       COALESCE(mp.role,''), COALESCE(mp.source,''),
	       CASE WHEN m.movie_id IS NULL OR COALESCE(m.url,'')='' THEN 'partial' ELSE 'validated' END
FROM movie_people mp
LEFT JOIN movies m ON m.movie_id=mp.movie_id
WHERE mp.person_id=?`, root.TargetID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var targetID, label, targetURL, relation, source, state string
		if err := rows.Scan(&targetID, &label, &targetURL, &relation, &source, &state); err != nil {
			return err
		}
		if strings.TrimSpace(targetID) == "" && strings.TrimSpace(label) == "" && strings.TrimSpace(targetURL) == "" {
			continue
		}
		addProjectedCard(projected, Card{Kind: "movie", TargetID: targetID, TargetLabel: label, TargetURL: targetURL, Depth: 1, RootIDs: root.RootIDs, RelationType: relation, RelationSource: source, ValidationState: state, ProvenanceURLs: []string{targetURL, root.TargetURL}})
	}
	return rows.Err()
}

func projectDirectCredits(db *sql.DB, root CardRoot, projected map[string]*Card) error {
	rows, err := db.Query(`
SELECT target_kind, COALESCE(target_id,''), target_label, COALESCE(target_url,''),
       relation_type, source, validation_state, provenance_json
FROM movie_related_credits
WHERE movie_id=?`, root.TargetID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, targetID, label, targetURL, relation, source, state, provenanceJSON string
		if err := rows.Scan(&kind, &targetID, &label, &targetURL, &relation, &source, &state, &provenanceJSON); err != nil {
			return err
		}
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind != "music" && kind != "source_work" && kind != "unresolved_credit" {
			continue
		}
		if !projectionStateAllowed(state) {
			continue
		}
		provenance := []string{}
		if strings.TrimSpace(provenanceJSON) != "" {
			if err := json.Unmarshal([]byte(provenanceJSON), &provenance); err != nil {
				return fmt.Errorf("decode credit %q provenance: %w", targetID, err)
			}
		}
		addProjectedCard(projected, Card{Kind: kind, TargetID: targetID, TargetLabel: label, TargetURL: targetURL, Depth: 1, RootIDs: root.RootIDs, RelationType: relation, RelationSource: source, ValidationState: state, ProvenanceURLs: provenance})
	}
	return rows.Err()
}
