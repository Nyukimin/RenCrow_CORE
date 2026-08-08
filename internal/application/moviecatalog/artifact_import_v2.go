package moviecatalog

import (
	"bufio"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// MovieCatalogGraphV2Schema is the canonical schema identifier shared with
// the CORE/Tools contract.  The older path-style identifier and the Tools
// transport's compact "2" spelling remain import aliases.
const MovieCatalogGraphV2Schema = "rencrow.movie_catalog.v2"

const movieCatalogGraphToolsV2Schema = "2"

type movieArtifactLine struct {
	lineNo        int
	raw           []byte
	recordType    string
	kind          string
	schemaVersion string
}

type movieArtifactEnvelope struct {
	RecordType    string `json:"record_type"`
	Kind          string `json:"kind"`
	SchemaVersion string `json:"schema_version"`
}

func readMovieArtifactLines(reader interface{ Read([]byte) (int, error) }) ([]movieArtifactLine, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	lines := []movieArtifactLine{}
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		raw := []byte(line)
		var envelope movieArtifactEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("decode movie catalog artifact line %d: %w", lineNo, err)
		}
		lines = append(lines, movieArtifactLine{
			lineNo:        lineNo,
			raw:           raw,
			recordType:    strings.ToLower(strings.TrimSpace(envelope.RecordType)),
			kind:          strings.ToLower(strings.TrimSpace(envelope.Kind)),
			schemaVersion: strings.TrimSpace(envelope.SchemaVersion),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read movie catalog artifact: %w", err)
	}
	return lines, nil
}

func movieArtifactIsV2(lines []movieArtifactLine) bool {
	for _, line := range lines {
		if line.recordType != "" || line.schemaVersion != "" {
			return true
		}
		switch line.kind {
		case "manifest", "node", "edge":
			return true
		}
	}
	return false
}

type movieCatalogGraphManifest struct {
	manifestID      string
	rootNodeIDs     []string
	rootNodeID      string
	rootKind        string
	rootID          string
	rootLabel       string
	rootURL         string
	sourceURL       string
	validationState string
	provenanceURLs  []string
	nodeCount       *int
	edgeCount       *int
}

type movieCatalogGraphNode struct {
	nodeID          string
	kind            string
	targetID        string
	label           string
	url             string
	synopsis        string
	biography       string
	validationState string
	provenanceURLs  []string
	depth           *int
	isD0            *bool
}

type movieCatalogGraphEdge struct {
	edgeID          string
	fromNodeID      string
	toNodeID        string
	fromKind        string
	toKind          string
	relationType    string
	source          string
	validationState string
	provenanceURLs  []string
	depth           *int
}

type movieCatalogGraph struct {
	manifest movieCatalogGraphManifest
	nodes    []movieCatalogGraphNode
	edges    []movieCatalogGraphEdge
}

type graphManifestWire struct {
	RecordType      string                  `json:"record_type"`
	SchemaVersion   string                  `json:"schema_version"`
	ArtifactID      string                  `json:"artifact_id"`
	ManifestID      string                  `json:"manifest_id"`
	RootNodeIDs     []string                `json:"root_node_ids"`
	RootNodeID      string                  `json:"root_node_id"`
	RootKind        string                  `json:"root_kind"`
	RootID          string                  `json:"root_id"`
	RootLabel       string                  `json:"root_label"`
	RootURL         string                  `json:"root_url"`
	SourceURL       string                  `json:"source_url"`
	ValidationState string                  `json:"validation_state"`
	ProvenanceURLs  []string                `json:"provenance_urls"`
	ProvenanceURL   string                  `json:"provenance_url"`
	NodeCount       *int                    `json:"node_count"`
	EdgeCount       *int                    `json:"edge_count"`
	Input           graphManifestInput      `json:"input"`
	Kind            string                  `json:"kind"`
	Schema          string                  `json:"schema"`
	ArtifactKind    string                  `json:"artifact_kind"`
	Status          string                  `json:"status"`
	MaxEntityDepth  int                     `json:"max_entity_depth"`
	Root            graphManifestRoot       `json:"root"`
	D0              *graphManifestRoot      `json:"d0"`
	Provenance      graphManifestProvenance `json:"provenance"`
	Pages           []graphManifestPage     `json:"pages"`
	Counts          graphManifestCounts     `json:"counts"`
}

type graphManifestRoot struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

type graphManifestProvenance struct {
	Resolver    string `json:"resolver"`
	IndexURL    string `json:"index_url"`
	UserAgent   string `json:"user_agent"`
	RetrievedAt string `json:"retrieved_at"`
	RobotsURL   string `json:"robots_url"`
}

type graphManifestPage struct {
	URL string `json:"url"`
}

type graphManifestCounts struct {
	Nodes  int `json:"nodes"`
	Edges  int `json:"edges"`
	Depth0 int `json:"depth0"`
	Depth1 int `json:"depth1"`
	Pages  int `json:"pages"`
}

type graphManifestInput struct {
	Kind     string `json:"kind"`
	Query    string `json:"query"`
	SeedURL  string `json:"seed_url"`
	TargetID string `json:"target_id"`
}

type graphNodeWire struct {
	RecordType      string          `json:"record_type"`
	SchemaVersion   string          `json:"schema_version"`
	NodeID          string          `json:"node_id"`
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	NodeKind        string          `json:"node_kind"`
	Type            string          `json:"type"`
	TargetID        string          `json:"target_id"`
	EntityID        string          `json:"entity_id"`
	Label           string          `json:"label"`
	Title           string          `json:"title"`
	Name            string          `json:"name"`
	URL             string          `json:"url"`
	Synopsis        string          `json:"synopsis"`
	Biography       string          `json:"biography"`
	Source          string          `json:"source"`
	ValidationState string          `json:"validation_state"`
	State           string          `json:"state"`
	ProvenanceURLs  []string        `json:"provenance_urls"`
	ProvenanceURL   string          `json:"provenance_url"`
	Depth           *int            `json:"depth"`
	EntityDepth     *int            `json:"entity_depth"`
	IsD0            *bool           `json:"is_d0"`
	Outbound        json.RawMessage `json:"outbound"`
	OutboundEdges   json.RawMessage `json:"outbound_edges"`
	OutgoingEdges   json.RawMessage `json:"outgoing_edges"`
	Outgoing        json.RawMessage `json:"outgoing"`
	Children        json.RawMessage `json:"children"`
	Edges           json.RawMessage `json:"edges"`
	Relations       json.RawMessage `json:"relations"`
}

type graphEdgeWire struct {
	RecordType      string   `json:"record_type"`
	SchemaVersion   string   `json:"schema_version"`
	EdgeID          string   `json:"edge_id"`
	ID              string   `json:"id"`
	FromNodeID      string   `json:"from_node_id"`
	ToNodeID        string   `json:"to_node_id"`
	From            string   `json:"from"`
	To              string   `json:"to"`
	FromKind        string   `json:"from_kind"`
	ToKind          string   `json:"to_kind"`
	RelationType    string   `json:"relation_type"`
	Relation        string   `json:"relation"`
	Type            string   `json:"type"`
	Source          string   `json:"source"`
	ValidationState string   `json:"validation_state"`
	ProvenanceURLs  []string `json:"provenance_urls"`
	ProvenanceURL   string   `json:"provenance_url"`
	EntityDepth     *int     `json:"entity_depth"`
	SourcePageURL   string   `json:"source_page_url"`
	Role            string   `json:"role"`
}

func importMovieCatalogGraphV2(ctx context.Context, db *sql.DB, lines []movieArtifactLine, sourceURL string) (CatalogImportResult, error) {
	graph, err := parseMovieCatalogGraphV2(lines)
	if err != nil {
		return CatalogImportResult{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return CatalogImportResult{}, fmt.Errorf("begin movie catalog graph import: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	result, err := persistMovieCatalogGraphV2(ctx, tx, graph, len(lines))
	if err != nil {
		return CatalogImportResult{}, fmt.Errorf("persist movie catalog graph: %w", err)
	}
	effectiveSourceURL := strings.TrimSpace(sourceURL)
	if effectiveSourceURL == "" {
		effectiveSourceURL = graph.manifest.sourceURL
	}
	if effectiveSourceURL != "" {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO fetch_log(url,status,error) VALUES(?,?,?)`, effectiveSourceURL, "ok", ""); err != nil {
			return CatalogImportResult{}, fmt.Errorf("record movie catalog fetch: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return CatalogImportResult{}, fmt.Errorf("commit movie catalog graph import: %w", err)
	}
	rollback = false
	return result, nil
}

func parseMovieCatalogGraphV2(lines []movieArtifactLine) (movieCatalogGraph, error) {
	graph := movieCatalogGraph{}
	manifestCount := 0
	seenSchema := ""
	seenNodeID := map[string]bool{}
	seenTargets := map[string]bool{}
	seenEdgeID := map[string]bool{}
	for _, line := range lines {
		version := normalizeMovieCatalogGraphSchema(line.schemaVersion)
		if version == "" {
			return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: schema_version is required", line.lineNo)
		}
		if seenSchema == "" {
			seenSchema = version
		} else if seenSchema != version {
			return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: mixed schema versions", line.lineNo)
		}

		recordType := line.recordType
		if recordType == "" {
			recordType = line.kind
		}
		switch recordType {
		case "manifest":
			manifestCount++
			if manifestCount > 1 {
				return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: exactly one manifest is required", line.lineNo)
			}
			manifest, err := decodeGraphManifest(line)
			if err != nil {
				return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: %w", line.lineNo, err)
			}
			graph.manifest = manifest
		case "node":
			node, err := decodeGraphNode(line)
			if err != nil {
				return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: %w", line.lineNo, err)
			}
			if seenNodeID[node.nodeID] {
				return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: duplicate node_id %q", line.lineNo, node.nodeID)
			}
			seenNodeID[node.nodeID] = true
			if node.targetID != "" {
				key := node.kind + "\x00" + node.targetID
				if seenTargets[key] {
					return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: duplicate target %s:%s", line.lineNo, node.kind, node.targetID)
				}
				seenTargets[key] = true
			} else {
				key := node.kind + "\x00" + normalizeCardText(node.label) + "\x00" + strings.Join(node.provenanceURLs, "\x00")
				if seenTargets[key] {
					return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: duplicate partial node %q", line.lineNo, node.label)
				}
				seenTargets[key] = true
			}
			graph.nodes = append(graph.nodes, node)
		case "edge":
			edge, err := decodeGraphEdge(line)
			if err != nil {
				return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: %w", line.lineNo, err)
			}
			if edge.edgeID == "" {
				edge.edgeID = graphEdgeStableID(edge)
			}
			if seenEdgeID[edge.edgeID] {
				return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: duplicate edge_id %q", line.lineNo, edge.edgeID)
			}
			seenEdgeID[edge.edgeID] = true
			graph.edges = append(graph.edges, edge)
		default:
			return movieCatalogGraph{}, fmt.Errorf("movie catalog graph line %d: unsupported record_type %q", line.lineNo, recordType)
		}
	}
	if manifestCount != 1 {
		return movieCatalogGraph{}, fmt.Errorf("movie catalog graph requires exactly one manifest")
	}
	if err := validateMovieCatalogGraphV2(&graph); err != nil {
		return movieCatalogGraph{}, err
	}
	return graph, nil
}

func normalizeMovieCatalogGraphSchema(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case MovieCatalogGraphV2Schema, "movie-catalog-graph/v2", movieCatalogGraphToolsV2Schema:
		return MovieCatalogGraphV2Schema
	default:
		return ""
	}
}

func movieCatalogGraphToolsV2(line movieArtifactLine) bool {
	return strings.TrimSpace(line.schemaVersion) == movieCatalogGraphToolsV2Schema
}

func decodeGraphManifest(line movieArtifactLine) (movieCatalogGraphManifest, error) {
	var wire graphManifestWire
	if err := json.Unmarshal(line.raw, &wire); err != nil {
		return movieCatalogGraphManifest{}, err
	}
	toolsV2 := movieCatalogGraphToolsV2(line)
	strict := !toolsV2
	recordTypeStrict := strings.TrimSpace(line.recordType) != "" && !toolsV2
	manifestID := strings.TrimSpace(wire.ArtifactID)
	if recordTypeStrict && manifestID == "" {
		return movieCatalogGraphManifest{}, fmt.Errorf("manifest artifact_id is required")
	}
	if manifestID == "" {
		manifestID = strings.TrimSpace(wire.ManifestID)
	}
	rootKind := strings.ToLower(strings.TrimSpace(wire.RootKind))
	rootID := strings.TrimSpace(wire.RootID)
	rootLabel := strings.TrimSpace(wire.RootLabel)
	rootURL := strings.TrimSpace(wire.RootURL)
	if toolsV2 {
		if rootKind == "" {
			rootKind = strings.ToLower(strings.TrimSpace(wire.Root.Kind))
			if rootKind == "" && wire.D0 != nil {
				rootKind = strings.ToLower(strings.TrimSpace(wire.D0.Kind))
			}
		}
		if rootID == "" {
			rootID = strings.TrimSpace(wire.Root.ID)
			if rootID == "" && wire.D0 != nil {
				rootID = strings.TrimSpace(wire.D0.ID)
			}
		}
		if rootLabel == "" {
			rootLabel = strings.TrimSpace(wire.Root.Label)
			if rootLabel == "" && wire.D0 != nil {
				rootLabel = strings.TrimSpace(wire.D0.Label)
			}
		}
		if rootURL == "" {
			rootURL = strings.TrimSpace(wire.Root.URL)
			if rootURL == "" && wire.D0 != nil {
				rootURL = strings.TrimSpace(wire.D0.URL)
			}
		}
	}
	rootNodeIDs := []string{}
	seenRootNodeIDs := map[string]bool{}
	for _, value := range wire.RootNodeIDs {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if seenRootNodeIDs[value] {
			return movieCatalogGraphManifest{}, fmt.Errorf("manifest contains duplicate root_node_id %q", value)
		}
		seenRootNodeIDs[value] = true
		rootNodeIDs = append(rootNodeIDs, value)
	}
	rootNodeID := strings.TrimSpace(wire.RootNodeID)
	if toolsV2 && rootNodeID == "" && rootKind != "" && rootID != "" {
		rootNodeID = rootKind + ":" + rootID
	}
	if len(rootNodeIDs) == 0 && rootNodeID != "" {
		rootNodeIDs = []string{rootNodeID}
	}
	if len(rootNodeIDs) != 1 {
		return movieCatalogGraphManifest{}, fmt.Errorf("manifest must declare exactly one root_node_id")
	}
	provenance, err := graphProvenance(wire.ProvenanceURLs, wire.ProvenanceURL)
	if err != nil {
		return movieCatalogGraphManifest{}, err
	}
	if toolsV2 && len(provenance) == 0 {
		provenance = append(provenance, rootURL)
		for _, page := range wire.Pages {
			provenance = append(provenance, strings.TrimSpace(page.URL))
		}
		provenance = append(provenance, wire.Provenance.IndexURL)
		provenance, err = graphProvenance(provenance, wire.Provenance.RobotsURL)
		if err != nil {
			return movieCatalogGraphManifest{}, err
		}
	}
	if len(provenance) == 0 {
		return movieCatalogGraphManifest{}, fmt.Errorf("manifest provenance_urls is required")
	}
	if recordTypeStrict && (wire.NodeCount == nil || wire.EdgeCount == nil) {
		return movieCatalogGraphManifest{}, fmt.Errorf("manifest node_count and edge_count are required")
	}
	nodeCount := wire.NodeCount
	edgeCount := wire.EdgeCount
	if toolsV2 && (wire.Counts.Nodes > 0 || wire.Counts.Edges > 0 || wire.Counts.Depth0 > 0 || wire.Counts.Depth1 > 0 || wire.Counts.Pages > 0) {
		n := wire.Counts.Nodes
		e := wire.Counts.Edges
		nodeCount = &n
		edgeCount = &e
	}
	state := strings.ToLower(strings.TrimSpace(wire.ValidationState))
	if state == "" {
		if strict {
			return movieCatalogGraphManifest{}, fmt.Errorf("manifest validation_state is required")
		}
		state = "confirmed"
	}
	if !validGraphState(state, true) {
		return movieCatalogGraphManifest{}, fmt.Errorf("manifest validation_state %q is invalid", state)
	}
	if state != "validated" && state != "confirmed" {
		return movieCatalogGraphManifest{}, fmt.Errorf("manifest validation_state must be validated or confirmed")
	}
	if rootKind == "" {
		rootKind = strings.ToLower(strings.TrimSpace(wire.Input.Kind))
	}
	if rootID == "" {
		rootID = strings.TrimSpace(wire.Input.TargetID)
	}
	if manifestID == "" && toolsV2 {
		manifestID = rootNodeID
	}
	if manifestID == "" {
		return movieCatalogGraphManifest{}, fmt.Errorf("manifest artifact_id or manifest_id is required")
	}
	sourceURL := strings.TrimSpace(wire.SourceURL)
	if sourceURL == "" {
		sourceURL = strings.TrimSpace(wire.Input.SeedURL)
	}
	if toolsV2 && sourceURL == "" {
		sourceURL = rootURL
	}
	if sourceURL != "" {
		parsed, err := url.Parse(sourceURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return movieCatalogGraphManifest{}, fmt.Errorf("invalid manifest source_url %q", sourceURL)
		}
	}
	return movieCatalogGraphManifest{
		manifestID:      manifestID,
		rootNodeIDs:     rootNodeIDs,
		rootNodeID:      rootNodeIDs[0],
		rootKind:        rootKind,
		rootID:          rootID,
		rootLabel:       rootLabel,
		rootURL:         rootURL,
		sourceURL:       sourceURL,
		validationState: state,
		provenanceURLs:  provenance,
		nodeCount:       nodeCount,
		edgeCount:       edgeCount,
	}, nil
}

func decodeGraphNode(line movieArtifactLine) (movieCatalogGraphNode, error) {
	var wire graphNodeWire
	if err := json.Unmarshal(line.raw, &wire); err != nil {
		return movieCatalogGraphNode{}, err
	}
	if graphNodeHasOutbound(line.raw) || hasGraphOutbound(wire.Outbound, wire.OutboundEdges, wire.OutgoingEdges, wire.Outgoing, wire.Children, wire.Edges, wire.Relations) {
		return movieCatalogGraphNode{}, fmt.Errorf("node must not contain outbound edge representation")
	}
	toolsV2 := movieCatalogGraphToolsV2(line)
	strict := !toolsV2
	nodeID := strings.TrimSpace(wire.NodeID)
	if nodeID == "" {
		nodeID = strings.TrimSpace(wire.ID)
	}
	if nodeID == "" {
		return movieCatalogGraphNode{}, fmt.Errorf("node_id is required")
	}
	kind := strings.ToLower(strings.TrimSpace(wire.Kind))
	if kind == "node" || kind == "" {
		kind = strings.ToLower(strings.TrimSpace(wire.NodeKind))
	}
	if kind == "" {
		kind = strings.ToLower(strings.TrimSpace(wire.Type))
	}
	if !validGraphNodeKind(kind) {
		return movieCatalogGraphNode{}, fmt.Errorf("node kind %q is invalid", kind)
	}
	targetID := strings.TrimSpace(wire.TargetID)
	if targetID == "" {
		targetID = strings.TrimSpace(wire.EntityID)
	}
	// Tools uses entity_id as a local label-derived node identity for work
	// credits.  It is not a validated catalog target_id and must not be
	// promoted into the public card identity.
	if toolsV2 && kind != "movie" && kind != "person" {
		targetID = ""
	}
	label := strings.TrimSpace(wire.Label)
	if label == "" {
		label = strings.TrimSpace(wire.Title)
	}
	if label == "" {
		label = strings.TrimSpace(wire.Name)
	}
	if label == "" {
		return movieCatalogGraphNode{}, fmt.Errorf("node label is required")
	}
	nodeURL := strings.TrimSpace(wire.URL)
	state := strings.ToLower(strings.TrimSpace(wire.ValidationState))
	if state == "" {
		state = strings.ToLower(strings.TrimSpace(wire.State))
	}
	if state == "" {
		if strict {
			return movieCatalogGraphNode{}, fmt.Errorf("node validation_state is required")
		}
		state = defaultGraphNodeState(kind, toolsV2)
	}
	if !validGraphState(state, false) {
		return movieCatalogGraphNode{}, fmt.Errorf("node validation_state %q is invalid", state)
	}
	if targetID == "" && (kind == "movie" || kind == "person") && state != "partial" && state != "unresolved" {
		targetID = graphTargetIDFromNodeID(nodeID, kind)
	}
	if (kind == "music" || kind == "source_work" || kind == "unresolved_credit") && (state == "partial" || state == "unresolved") {
		// A partial/unresolved credit may retain its explicit label and URL, but
		// its catalog identity is not established by the label alone.
		targetID = ""
	}
	provenance, err := graphProvenance(wire.ProvenanceURLs, wire.ProvenanceURL)
	if err != nil {
		return movieCatalogGraphNode{}, err
	}
	if len(provenance) == 0 && strict {
		return movieCatalogGraphNode{}, fmt.Errorf("node provenance_urls is required")
	}
	partialEntity := (kind == "movie" || kind == "person") && (state == "partial" || state == "unresolved")
	if (kind == "movie" || kind == "person") && !partialEntity && (targetID == "" || nodeURL == "") {
		return movieCatalogGraphNode{}, fmt.Errorf("%s node requires target_id and url", kind)
	}
	if nodeURL == "" && len(provenance) == 0 && strict {
		return movieCatalogGraphNode{}, fmt.Errorf("node url or provenance is required")
	}
	if err := validateGraphKindURL(kind, targetID, nodeURL); err != nil {
		return movieCatalogGraphNode{}, err
	}
	return movieCatalogGraphNode{
		nodeID:          nodeID,
		kind:            kind,
		targetID:        targetID,
		label:           label,
		url:             nodeURL,
		synopsis:        strings.TrimSpace(wire.Synopsis),
		biography:       strings.TrimSpace(wire.Biography),
		validationState: state,
		provenanceURLs:  provenance,
		depth:           firstGraphDepth(wire.Depth, wire.EntityDepth),
		isD0:            wire.IsD0,
	}, nil
}

func decodeGraphEdge(line movieArtifactLine) (movieCatalogGraphEdge, error) {
	var wire graphEdgeWire
	if err := json.Unmarshal(line.raw, &wire); err != nil {
		return movieCatalogGraphEdge{}, err
	}
	toolsV2 := movieCatalogGraphToolsV2(line)
	strict := !toolsV2
	edgeID := strings.TrimSpace(wire.EdgeID)
	if edgeID == "" {
		edgeID = strings.TrimSpace(wire.ID)
	}
	if strict && edgeID == "" {
		return movieCatalogGraphEdge{}, fmt.Errorf("edge_id is required")
	}
	fromNodeID := strings.TrimSpace(wire.FromNodeID)
	if fromNodeID == "" {
		fromNodeID = strings.TrimSpace(wire.From)
	}
	toNodeID := strings.TrimSpace(wire.ToNodeID)
	if toNodeID == "" {
		toNodeID = strings.TrimSpace(wire.To)
	}
	if fromNodeID == "" || toNodeID == "" {
		return movieCatalogGraphEdge{}, fmt.Errorf("edge from_node_id and to_node_id are required")
	}
	relation := strings.TrimSpace(wire.RelationType)
	if relation == "" {
		relation = strings.TrimSpace(wire.Relation)
	}
	if relation == "" {
		relation = strings.TrimSpace(wire.Type)
	}
	if relation == "" {
		return movieCatalogGraphEdge{}, fmt.Errorf("edge relation_type is required")
	}
	state := strings.ToLower(strings.TrimSpace(wire.ValidationState))
	if state == "" {
		if strict {
			return movieCatalogGraphEdge{}, fmt.Errorf("edge validation_state is required")
		}
		state = "validated"
	}
	if state != "validated" {
		return movieCatalogGraphEdge{}, fmt.Errorf("edge validation_state must be validated")
	}
	provenanceValues := append([]string{}, wire.ProvenanceURLs...)
	provenanceValues = append(provenanceValues, wire.SourcePageURL)
	provenance, err := graphProvenance(provenanceValues, wire.ProvenanceURL)
	if err != nil {
		return movieCatalogGraphEdge{}, err
	}
	if len(provenance) == 0 && strict {
		return movieCatalogGraphEdge{}, fmt.Errorf("edge provenance_urls is required")
	}
	source := strings.TrimSpace(wire.Source)
	if source == "" {
		source = "artifact"
	}
	return movieCatalogGraphEdge{
		edgeID:          edgeID,
		fromNodeID:      fromNodeID,
		toNodeID:        toNodeID,
		fromKind:        strings.ToLower(strings.TrimSpace(wire.FromKind)),
		toKind:          strings.ToLower(strings.TrimSpace(wire.ToKind)),
		relationType:    relation,
		source:          source,
		validationState: state,
		provenanceURLs:  provenance,
		depth:           wire.EntityDepth,
	}, nil
}

func defaultGraphNodeState(kind string, toolsV2 bool) string {
	if toolsV2 {
		switch kind {
		case "unresolved_credit":
			return "unresolved"
		case "music", "source_work":
			return "partial"
		}
	}
	return "validated"
}

func firstGraphDepth(depth, entityDepth *int) *int {
	if depth != nil {
		return depth
	}
	return entityDepth
}

func validateMovieCatalogGraphV2(graph *movieCatalogGraph) error {
	manifest := &graph.manifest
	if manifest.nodeCount != nil && *manifest.nodeCount != len(graph.nodes) {
		return fmt.Errorf("manifest node_count=%d does not match %d nodes", *manifest.nodeCount, len(graph.nodes))
	}
	if manifest.edgeCount != nil && *manifest.edgeCount != len(graph.edges) {
		return fmt.Errorf("manifest edge_count=%d does not match %d edges", *manifest.edgeCount, len(graph.edges))
	}
	if len(graph.nodes) == 0 {
		return fmt.Errorf("movie catalog graph contains no nodes")
	}
	nodes := make(map[string]*movieCatalogGraphNode, len(graph.nodes))
	for i := range graph.nodes {
		node := &graph.nodes[i]
		if _, exists := nodes[node.nodeID]; exists {
			return fmt.Errorf("duplicate node_id %q", node.nodeID)
		}
		nodes[node.nodeID] = node
		if node.depth != nil && (*node.depth < 0 || *node.depth > 1) {
			return fmt.Errorf("node %q depth must be 0 or 1", node.nodeID)
		}
	}
	root, ok := nodes[manifest.rootNodeID]
	if !ok {
		return fmt.Errorf("manifest root_node_id %q is not present", manifest.rootNodeID)
	}
	if manifest.rootKind != "" && strings.ToLower(manifest.rootKind) != root.kind {
		return fmt.Errorf("manifest root kind %q does not match node kind %q", manifest.rootKind, root.kind)
	}
	if root.kind != "movie" && root.kind != "person" {
		return fmt.Errorf("root node kind %q must be movie or person", root.kind)
	}
	if root.targetID == "" || root.url == "" {
		return fmt.Errorf("root node %q requires target_id and url", root.nodeID)
	}
	if manifest.rootID != "" && manifest.rootID != root.targetID {
		return fmt.Errorf("manifest root_id %q does not match node target_id %q", manifest.rootID, root.targetID)
	}
	if manifest.rootLabel != "" && manifest.rootLabel != root.label {
		return fmt.Errorf("manifest root_label does not match root node")
	}
	if manifest.rootURL != "" && manifest.rootURL != root.url {
		return fmt.Errorf("manifest root_url does not match root node")
	}
	if root.depth != nil && *root.depth != 0 {
		return fmt.Errorf("root node %q must have depth 0", root.nodeID)
	}
	root.depth = intPointer(0)
	if root.isD0 != nil && !*root.isD0 {
		return fmt.Errorf("root node %q must declare is_d0=true", root.nodeID)
	}
	if root.isD0 == nil {
		root.isD0 = boolPointer(true)
	}
	depthZero := 0
	for i := range graph.nodes {
		node := &graph.nodes[i]
		if node.nodeID != root.nodeID {
			if node.isD0 != nil && *node.isD0 {
				return fmt.Errorf("non-root node %q must declare is_d0=false", node.nodeID)
			}
			if node.isD0 == nil {
				node.isD0 = boolPointer(false)
			}
			if node.depth == nil {
				node.depth = intPointer(1)
			} else if *node.depth != 1 {
				return fmt.Errorf("non-root node %q must have depth 1", node.nodeID)
			}
		}
		if *node.depth == 0 {
			depthZero++
		}
	}
	if depthZero != 1 {
		return fmt.Errorf("movie catalog graph requires exactly one depth-0 node")
	}

	inbound := map[string]int{}
	seenEdges := map[string]bool{}
	for i := range graph.edges {
		edge := &graph.edges[i]
		from, fromOK := nodes[edge.fromNodeID]
		to, toOK := nodes[edge.toNodeID]
		if !fromOK || !toOK {
			return fmt.Errorf("edge %q references an unknown node", edge.edgeID)
		}
		if from.depth == nil || to.depth == nil || *from.depth != 0 || *to.depth != 1 {
			return fmt.Errorf("edge %q must be D0 to D1", edge.edgeID)
		}
		if edge.depth != nil && *edge.depth != 1 {
			return fmt.Errorf("edge %q entity_depth must be 1", edge.edgeID)
		}
		if edge.fromKind != "" && edge.fromKind != from.kind {
			return fmt.Errorf("edge %q from_kind does not match source node", edge.edgeID)
		}
		if edge.toKind != "" && edge.toKind != to.kind {
			return fmt.Errorf("edge %q to_kind does not match target node", edge.edgeID)
		}
		if !validGraphEdgeKinds(from.kind, to.kind) {
			return fmt.Errorf("edge %q relation %s -> %s is not a supported direct relation", edge.edgeID, from.kind, to.kind)
		}
		key := edge.fromNodeID + "\x00" + edge.toNodeID + "\x00" + edge.relationType + "\x00" + edge.source
		if seenEdges[key] {
			return fmt.Errorf("duplicate graph edge %s -> %s", edge.fromNodeID, edge.toNodeID)
		}
		seenEdges[key] = true
		inbound[edge.toNodeID]++
	}
	// Tools' compact transport carries provenance on the manifest or edge,
	// while the canonical shape carries it on each node.  Normalize the
	// former into the same in-memory graph before persistence.
	for i := range graph.nodes {
		node := &graph.nodes[i]
		if len(node.provenanceURLs) != 0 {
			continue
		}
		for _, edge := range graph.edges {
			if edge.toNodeID == node.nodeID && len(edge.provenanceURLs) != 0 {
				node.provenanceURLs = append([]string{}, edge.provenanceURLs...)
				break
			}
		}
		if len(node.provenanceURLs) == 0 {
			node.provenanceURLs = append([]string{}, manifest.provenanceURLs...)
		}
	}
	for _, node := range graph.nodes {
		if node.nodeID != root.nodeID && inbound[node.nodeID] == 0 {
			return fmt.Errorf("D1 node %q is not connected to the root", node.nodeID)
		}
	}
	return nil
}

func persistMovieCatalogGraphV2(ctx context.Context, tx *sql.Tx, graph movieCatalogGraph, recordCount int) (CatalogImportResult, error) {
	result := CatalogImportResult{Records: recordCount, Edges: len(graph.edges)}
	nodes := make(map[string]movieCatalogGraphNode, len(graph.nodes))
	for _, node := range graph.nodes {
		nodes[node.nodeID] = node
		switch node.kind {
		case "movie":
			if node.targetID == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO movies(movie_id,title,url,synopsis) VALUES(?,?,?,?)`, node.targetID, node.label, node.url, node.synopsis); err != nil {
				return CatalogImportResult{}, err
			}
			result.Movies++
		case "person":
			if node.targetID == "" {
				continue
			}
			profile := "{}"
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO people(person_id,name,url,profile_json,biography) VALUES(?,?,?,?,?)`, node.targetID, node.label, node.url, profile, node.biography); err != nil {
				return CatalogImportResult{}, err
			}
			result.People++
		}
	}
	for _, edge := range graph.edges {
		from := nodes[edge.fromNodeID]
		to := nodes[edge.toNodeID]
		provenance := edge.provenanceURLs
		if len(provenance) == 0 {
			provenance = to.provenanceURLs
		}
		provenanceJSON, err := json.Marshal(provenance)
		if err != nil {
			return CatalogImportResult{}, err
		}
		switch {
		case from.kind == "movie" && to.kind == "person":
			if _, err := tx.ExecContext(ctx, `DELETE FROM movie_people WHERE movie_id=? AND person_id=? AND role=? AND source=?`, from.targetID, to.targetID, edge.relationType, edge.source); err != nil {
				return CatalogImportResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url) VALUES(?,?,?,?,?,?,?,?)`, from.targetID, to.targetID, edge.relationType, edge.source, from.label, to.label, from.url, to.url); err != nil {
				return CatalogImportResult{}, err
			}
		case from.kind == "person" && to.kind == "movie":
			if _, err := tx.ExecContext(ctx, `DELETE FROM movie_people WHERE movie_id=? AND person_id=? AND role=? AND source=?`, to.targetID, from.targetID, edge.relationType, edge.source); err != nil {
				return CatalogImportResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url) VALUES(?,?,?,?,?,?,?,?)`, to.targetID, from.targetID, edge.relationType, edge.source, to.label, from.label, to.url, from.url); err != nil {
				return CatalogImportResult{}, err
			}
		case from.kind == "movie" && (to.kind == "music" || to.kind == "source_work" || to.kind == "unresolved_credit"):
			creditID := edge.edgeID
			if creditID == "" {
				creditID = graphEdgeStableID(edge)
			}
			var targetID any
			if to.targetID != "" {
				targetID = to.targetID
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO movie_related_credits(credit_id,movie_id,target_kind,target_id,target_label,target_url,relation_type,source,validation_state,provenance_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, creditID, from.targetID, to.kind, targetID, to.label, to.url, edge.relationType, edge.source, to.validationState, string(provenanceJSON)); err != nil {
				return CatalogImportResult{}, err
			}
		}
	}
	root := graph.manifest
	rootProvenanceJSON, err := json.Marshal(root.provenanceURLs)
	if err != nil {
		return CatalogImportResult{}, err
	}
	rootNode := nodes[root.rootNodeID]
	rootTargetID := root.rootID
	if rootTargetID == "" {
		rootTargetID = rootNode.targetID
	}
	rootKind := root.rootKind
	if rootKind == "" {
		rootKind = rootNode.kind
	}
	rootLabel := root.rootLabel
	if rootLabel == "" {
		rootLabel = rootNode.label
	}
	rootURL := root.rootURL
	if rootURL == "" {
		rootURL = rootNode.url
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO movie_catalog_roots(root_id,manifest_id,kind,target_id,target_label,target_url,validation_state,provenance_json,source_url,updated_at) VALUES(?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)`, root.manifestID, root.manifestID, rootKind, rootTargetID, rootLabel, rootURL, root.validationState, string(rootProvenanceJSON), root.sourceURL); err != nil {
		return CatalogImportResult{}, err
	}
	return result, nil
}

func graphProvenance(values []string, single string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range append(append([]string{}, values...), single) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("invalid provenance URL %q", value)
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out, nil
}

func validGraphState(value string, manifest bool) bool {
	switch value {
	case "validated", "partial", "unresolved", "confirmed", "ready", "active":
		return true
	default:
		return false
	}
}

func validGraphNodeKind(value string) bool {
	switch value {
	case "movie", "person", "music", "source_work", "unresolved_credit":
		return true
	default:
		return false
	}
}

func validGraphEdgeKinds(from, to string) bool {
	return (from == "movie" && to == "person") ||
		(from == "person" && to == "movie") ||
		(from == "movie" && (to == "music" || to == "source_work" || to == "unresolved_credit"))
}

func validateGraphKindURL(kind, targetID, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("node %s has invalid url %q", kind, value)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i, segment := range segments {
		segment = strings.ToLower(strings.TrimSpace(segment))
		if segment != "movie" && segment != "person" {
			continue
		}
		if segment != kind {
			return fmt.Errorf("node kind %q does not match url path %q", kind, parsed.Path)
		}
		if i+1 < len(segments) && targetID != "" && strings.TrimSpace(segments[i+1]) != targetID {
			return fmt.Errorf("node target_id %q does not match url %q", targetID, value)
		}
		return nil
	}
	if strings.EqualFold(parsed.Hostname(), "eiga.com") && (kind == "movie" || kind == "person") {
		return fmt.Errorf("node %s url must contain /%s/<id>/", kind, kind)
	}
	return nil
}

func hasGraphOutbound(values ...json.RawMessage) bool {
	for _, value := range values {
		if len(value) == 0 || strings.TrimSpace(string(value)) == "null" {
			continue
		}
		return true
	}
	return false
}

func graphNodeHasOutbound(raw []byte) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false
	}
	for key, value := range fields {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "outbound" && key != "outbound_edges" && key != "outgoing" && key != "outgoing_edges" && key != "children" && key != "edges" && key != "relations" && !strings.HasPrefix(key, "outbound_") && !strings.HasPrefix(key, "outgoing_") {
			continue
		}
		if len(value) > 0 && strings.TrimSpace(string(value)) != "null" {
			return true
		}
	}
	return false
}

func graphTargetIDFromNodeID(nodeID, kind string) string {
	prefix := kind + ":"
	if strings.HasPrefix(strings.ToLower(nodeID), prefix) {
		return strings.TrimSpace(nodeID[len(prefix):])
	}
	return ""
}

func graphEdgeStableID(edge movieCatalogGraphEdge) string {
	h := sha1.New()
	_, _ = h.Write([]byte(edge.fromNodeID + "\x00" + edge.toNodeID + "\x00" + edge.relationType + "\x00" + edge.source))
	return "edge_" + hex.EncodeToString(h.Sum(nil)[:10])
}

func intPointer(value int) *int {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}

func normalizeCardText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}
