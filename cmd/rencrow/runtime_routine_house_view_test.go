package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
)

const routineHouseViewTestSceneSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var routineHouseViewTestActors = []string{"mio", "shiro", "midori", "kuro", "aka", "ao", "kin", "gin"}

func TestRoutineHouseViewReturnsBoundedCanonicalWorldProjection(t *testing.T) {
	cfg, deps, server := newRoutineHouseViewTestRuntime(t, true)
	defer server.Close()

	handler := withInteractionProfileGuard(runtimeRoutineHouseViewHandler(cfg, deps))
	recorder := serveRoutineHouseViewRequest(handler, http.MethodGet, "/viewer/world/home", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := sortedJSONKeys(body), []string{"actors", "observed_at", "scene_sha256", "state_seq"}; !equalStrings(got, want) {
		t.Fatalf("response keys = %v, want %v", got, want)
	}
	var sceneSHA string
	if err := json.Unmarshal(body["scene_sha256"], &sceneSHA); err != nil || sceneSHA != routineHouseViewTestSceneSHA256 {
		t.Fatalf("scene_sha256 = %q, err=%v", sceneSHA, err)
	}
	var stateSeq uint64
	if err := json.Unmarshal(body["state_seq"], &stateSeq); err != nil || stateSeq != 44 {
		t.Fatalf("state_seq = %d, err=%v; want 44", stateSeq, err)
	}
	var observedAt string
	if err := json.Unmarshal(body["observed_at"], &observedAt); err != nil || observedAt != "2026-11-13T17:03:04.123456789Z" {
		t.Fatalf("observed_at = %q, err=%v", observedAt, err)
	}

	var actors []map[string]json.RawMessage
	if err := json.Unmarshal(body["actors"], &actors); err != nil {
		t.Fatalf("decode actors: %v", err)
	}
	if len(actors) != len(routineHouseViewTestActors) {
		t.Fatalf("actor count = %d, want %d", len(actors), len(routineHouseViewTestActors))
	}
	byID := make(map[string]map[string]json.RawMessage, len(actors))
	for _, actor := range actors {
		var id string
		if err := json.Unmarshal(actor["id"], &id); err != nil {
			t.Fatalf("decode actor id: %v", err)
		}
		byID[id] = actor
	}
	for _, id := range routineHouseViewTestActors {
		actor, ok := byID[id]
		if !ok {
			t.Fatalf("missing canonical actor %q", id)
		}
		var roomID string
		if err := json.Unmarshal(actor["room_id"], &roomID); err != nil || roomID != "room-"+id {
			t.Fatalf("actor %q room_id = %q, err=%v", id, roomID, err)
		}
		var position []float64
		if err := json.Unmarshal(actor["position"], &position); err != nil || len(position) != 3 {
			t.Fatalf("actor %q position = %v, err=%v", id, position, err)
		}
		if _, leaked := actor["activity_id"]; leaked {
			t.Fatalf("actor %q leaked private activity id", id)
		}
		if _, leaked := actor["world_actor_id"]; leaked {
			t.Fatalf("actor %q leaked WORLD actor id", id)
		}
		for key := range actor {
			switch key {
			case "id", "room_id", "position", "active_kind", "active_pose", "activity_started_at", "activity_due_at", "move_points", "target_room_id", "mirror_pose_completions":
			default:
				t.Fatalf("actor %q contains unapproved field %q", id, key)
			}
		}
	}

	var shiro map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshalJSON(t, byID["shiro"]), &shiro); err != nil {
		t.Fatalf("decode shiro: %v", err)
	}
	var activeKind, targetRoom string
	if err := json.Unmarshal(shiro["active_kind"], &activeKind); err != nil || activeKind != "move" {
		t.Fatalf("shiro active_kind = %q, err=%v", activeKind, err)
	}
	if err := json.Unmarshal(shiro["target_room_id"], &targetRoom); err != nil || targetRoom != "room-shiro-next" {
		t.Fatalf("shiro target_room_id = %q, err=%v", targetRoom, err)
	}
	var movePoints [][]float64
	if err := json.Unmarshal(shiro["move_points"], &movePoints); err != nil || len(movePoints) != 2 {
		t.Fatalf("shiro move_points = %v, err=%v", movePoints, err)
	}
	var kuro map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshalJSON(t, byID["kuro"]), &kuro); err != nil {
		t.Fatalf("decode kuro: %v", err)
	}
	var activePose string
	var mirrorCompletions uint64
	if err := json.Unmarshal(kuro["active_pose"], &activePose); err != nil || activePose != "mirror-greeting" {
		t.Fatalf("kuro active_pose = %q, err=%v", activePose, err)
	}
	if err := json.Unmarshal(kuro["mirror_pose_completions"], &mirrorCompletions); err != nil || mirrorCompletions != 2 {
		t.Fatalf("kuro mirror_pose_completions = %d, err=%v", mirrorCompletions, err)
	}
	if strings.Contains(recorder.Body.String(), "read-token") || strings.Contains(recorder.Body.String(), "state_hash") {
		t.Fatalf("response leaked WORLD credentials or unrelated snapshot fields: %s", recorder.Body.String())
	}
}

func TestRoutineHouseViewRetainsMirrorPoseCompletionsWhenKuroIsIdle(t *testing.T) {
	_, deps, server := newRoutineHouseViewTestRuntime(t, true)
	defer server.Close()

	actors := make([]worldread.Actor, 0, len(deps.agentRoutine.worldActorIDs))
	for worldID, coreID := range deps.agentRoutine.worldActorIDs {
		actor := worldread.Actor{
			ActorID: worldID, Present: true, RoomID: "room-" + coreID,
			Position: [3]float64{1, 2, 0}, HasPosition: true,
			HomeSceneSHA256: routineHouseViewTestSceneSHA256,
		}
		if coreID == "kuro" {
			actor.MirrorPoseCompletions = 7
		}
		actors = append(actors, actor)
	}
	projected, err := runtimeRoutineHouseViewProject(deps.agentRoutine, routineSnapshotRead{
		snapshot: worldread.Snapshot{WorldID: "home-world", StateSeq: 45, Actors: actors, HomeSceneSHA256: routineHouseViewTestSceneSHA256},
		readAt:   time.Date(2026, time.November, 14, 2, 4, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("project idle Kuro snapshot: %v", err)
	}
	var kuro runtimeRoutineHouseViewActorDTO
	for _, actor := range projected.Actors {
		if actor.ID == "kuro" {
			kuro = actor
			break
		}
	}
	if kuro.ActiveKind != "" {
		t.Fatalf("idle kuro active_kind = %q, want empty", kuro.ActiveKind)
	}
	if kuro.MirrorPoseCompletions == nil || *kuro.MirrorPoseCompletions != 7 {
		t.Fatalf("idle kuro mirror_pose_completions = %v, want 7", kuro.MirrorPoseCompletions)
	}
}

func TestRegisterFeatureRoutesRegistersHouseViewForEnabledRoutine(t *testing.T) {
	cfg := &config.Config{
		WorkspaceDir: t.TempDir(),
		Heartbeat:    config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{Enabled: true}},
	}
	deps := &Dependencies{eventHub: viewer.NewEventHub(10)}
	mux := http.NewServeMux()
	registerFeatureRoutes(mux, cfg, deps, sttRuntime{WSHandler: http.NotFoundHandler()}, voiceChatRuntime{WSHandler: http.NotFoundHandler()}, viewer.DebugSystemOptions{})

	recorder := serveRoutineHouseViewRequest(withInteractionProfileGuard(mux), http.MethodGet, "/viewer/world/home", true)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("registered route status = %d, want %d: %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestRoutineHouseViewRequiresExactLocalPortalRequest(t *testing.T) {
	cfg, deps, server := newRoutineHouseViewTestRuntime(t, true)
	defer server.Close()

	handler := withInteractionProfileGuard(withTailscaleViewerOnlyGuard(runtimeRoutineHouseViewHandler(cfg, deps)))
	tests := []struct {
		name       string
		method     string
		path       string
		client     string
		profile    string
		remoteAddr string
		host       string
		forwarded  bool
		funnel     bool
		want       int
	}{
		{name: "trusted local PORTAL", method: http.MethodGet, path: "/viewer/world/home", client: "RenCrow_PORTAL", profile: "portal-living", remoteAddr: "127.0.0.1:18790", host: "127.0.0.1:18790", want: http.StatusOK},
		{name: "no profile headers", method: http.MethodGet, path: "/viewer/world/home", remoteAddr: "127.0.0.1:18790", host: "127.0.0.1:18790", want: http.StatusNotFound},
		{name: "missing profile", method: http.MethodGet, path: "/viewer/world/home", client: "RenCrow_PORTAL", remoteAddr: "127.0.0.1:18790", host: "127.0.0.1:18790", want: http.StatusForbidden},
		{name: "profile spelling is exact", method: http.MethodGet, path: "/viewer/world/home", client: "RenCrow_PORTAL", profile: "Portal-Living", remoteAddr: "127.0.0.1:18790", host: "127.0.0.1:18790", want: http.StatusNotFound},
		{name: "wrong client", method: http.MethodGet, path: "/viewer/world/home", client: "RenCrow_CMD", profile: "portal-living", remoteAddr: "127.0.0.1:18790", host: "127.0.0.1:18790", want: http.StatusForbidden},
		{name: "wrong method", method: http.MethodPost, path: "/viewer/world/home", client: "RenCrow_PORTAL", profile: "portal-living", remoteAddr: "127.0.0.1:18790", host: "127.0.0.1:18790", want: http.StatusForbidden},
		{name: "query is rejected", method: http.MethodGet, path: "/viewer/world/home?actor=mio", client: "RenCrow_PORTAL", profile: "portal-living", remoteAddr: "127.0.0.1:18790", host: "127.0.0.1:18790", want: http.StatusBadRequest},
		{name: "encoded path is rejected", method: http.MethodGet, path: "/viewer/world/%68ome", client: "RenCrow_PORTAL", profile: "portal-living", remoteAddr: "127.0.0.1:18790", host: "127.0.0.1:18790", want: http.StatusNotFound},
		{name: "LAN spoofing is rejected", method: http.MethodGet, path: "/viewer/world/home", client: "RenCrow_PORTAL", profile: "portal-living", remoteAddr: "192.0.2.10:18790", host: "127.0.0.1:18790", want: http.StatusNotFound},
		{name: "proxy headers are rejected", method: http.MethodGet, path: "/viewer/world/home", client: "RenCrow_PORTAL", profile: "portal-living", remoteAddr: "127.0.0.1:18790", host: "127.0.0.1:18790", forwarded: true, want: http.StatusNotFound},
		{name: "Tailscale Funnel is rejected", method: http.MethodGet, path: "/viewer/world/home", client: "RenCrow_PORTAL", profile: "portal-living", remoteAddr: "127.0.0.1:18790", host: "viewer.example.ts.net", funnel: true, want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://"+tt.host+tt.path, nil)
			req.Host = tt.host
			req.RemoteAddr = tt.remoteAddr
			if tt.client != "" {
				req.Header.Set("X-RenCrow-Client", tt.client)
			}
			if tt.profile != "" {
				req.Header.Set(interactionProfileHeader, tt.profile)
			}
			if tt.forwarded {
				req.Header.Set("X-Forwarded-For", "192.0.2.10")
			}
			if tt.funnel {
				req.Header.Set("Tailscale-Funnel-Request", "true")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tt.want, recorder.Body.String())
			}
		})
	}
}

func TestRoutineHouseViewDistinguishesDisabledAndUnavailableWorld(t *testing.T) {
	disabled := runtimeRoutineHouseViewHandler(&config.Config{}, &Dependencies{})
	if got := serveRoutineHouseViewRequest(disabled, http.MethodGet, "/viewer/world/home", true).Code; got != http.StatusNotFound {
		t.Fatalf("disabled route status = %d, want %d", got, http.StatusNotFound)
	}

	cfg := &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{Enabled: true}}}
	unavailable := runtimeRoutineHouseViewHandler(cfg, &Dependencies{agentRoutine: &agentRoutineRuntime{cfg: cfg}})
	if got := serveRoutineHouseViewRequest(unavailable, http.MethodGet, "/viewer/world/home", true).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("unavailable WORLD status = %d, want %d", got, http.StatusServiceUnavailable)
	}

	incompleteCfg, incompleteDeps, incompleteServer := newRoutineHouseViewTestRuntime(t, false)
	defer incompleteServer.Close()
	incomplete := runtimeRoutineHouseViewHandler(incompleteCfg, incompleteDeps)
	if got := serveRoutineHouseViewRequest(incomplete, http.MethodGet, "/viewer/world/home", true).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("incomplete WORLD snapshot status = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

func newRoutineHouseViewTestRuntime(t *testing.T, includeAllActors bool) (*config.Config, *Dependencies, *httptest.Server) {
	t.Helper()
	worldActors := make(map[string]string, len(routineHouseViewTestActors))
	worldToCore := make(map[string]string, len(routineHouseViewTestActors))
	actors := make(map[string]any, len(routineHouseViewTestActors))
	movePoints := map[string]any{
		"home-shiro": [][]float64{{1.25, 2.5, 0}, {3, 2.5, 0}},
	}
	for i, coreID := range routineHouseViewTestActors {
		worldID := "home-" + coreID
		worldActors[coreID] = worldID
		worldToCore[worldID] = coreID
		if !includeAllActors && coreID == "gin" {
			continue
		}
		actor := map[string]any{
			"room_id":           "room-" + coreID,
			"position":          []float64{float64(i) + 0.25, 1.5, 0},
			"home_scene_sha256": routineHouseViewTestSceneSHA256,
		}
		if coreID == "shiro" {
			actor["activity_id"] = "move-shiro"
			actor["activity_kind"] = "move"
			actor["activity_started_at"] = "2026-11-14T02:00:00Z"
			actor["activity_due_at"] = "2026-11-14T02:00:10Z"
			actor["target_room_id"] = "room-shiro-next"
		}
		if coreID == "kuro" {
			actor["activity_id"] = "pose-kuro"
			actor["activity_kind"] = "mirror_pose"
			actor["pose_id"] = "mirror-greeting"
			actor["activity_started_at"] = "2026-11-14T02:01:00Z"
			actor["activity_due_at"] = "2026-11-14T02:01:08Z"
			actor["mirror_pose_completions"] = 2
		}
		actors[worldID] = actor
	}
	if !includeAllActors {
		delete(movePoints, "home-shiro")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer read-token" {
			t.Errorf("WORLD Authorization = %q, want configured read credential", got)
		}
		body := map[string]any{
			"schema_version":    1,
			"world_id":          "home-world",
			"timeline_id":       "home-timeline",
			"timeline_epoch":    "home-epoch",
			"state_seq":         44,
			"state_hash":        strings.Repeat("b", 64),
			"home_scene_sha256": routineHouseViewTestSceneSHA256,
			"home_move_points":  movePoints,
			"state":             map[string]any{"actors": actors, "activities": map[string]any{}, "resources": map[string]any{}, "reservations": []any{}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	client, err := worldread.New(worldread.Config{
		BaseURL: server.URL, Credential: "read-token", WorldID: "home-world", TimelineID: "home-timeline",
		TimelineEpoch: "home-epoch", ActorIDs: mapKeys(worldToCore), Timeout: time.Second,
	})
	if err != nil {
		server.Close()
		t.Fatalf("create WORLD client: %v", err)
	}
	cfg := &config.Config{Heartbeat: config.HeartbeatConfig{Routine: config.RoutineHeartbeatConfig{
		Enabled: true, WorldActors: worldActors,
	}}}
	runtime := &agentRoutineRuntime{
		cfg: cfg, world: client, worldActorIDs: worldToCore,
		now: func() time.Time {
			return time.Date(2026, time.November, 14, 2, 3, 4, 123456789, time.FixedZone("test", 9*60*60))
		},
	}
	return cfg, &Dependencies{agentRoutine: runtime}, server
}

func serveRoutineHouseViewRequest(handler http.Handler, method, target string, trusted bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:18790"+target, nil)
	req.Host = "127.0.0.1:18790"
	req.RemoteAddr = "127.0.0.1:18790"
	if trusted {
		req.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
		req.Header.Set(interactionProfileHeader, "portal-living")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func sortedJSONKeys(value map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test value: %v", err)
	}
	return body
}

func mapKeys(value map[string]string) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}
