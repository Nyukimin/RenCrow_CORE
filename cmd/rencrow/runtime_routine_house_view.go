package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
)

const runtimeRoutineHouseViewPath = "/viewer/world/home"

var errRoutineHouseViewSnapshotUnavailable = errors.New("WORLD home view snapshot is unavailable or incomplete")

type runtimeRoutineHouseViewResponse struct {
	SceneSHA256 string                            `json:"scene_sha256"`
	StateSeq    uint64                            `json:"state_seq"`
	ObservedAt  string                            `json:"observed_at"`
	Actors      []runtimeRoutineHouseViewActorDTO `json:"actors"`
}

type runtimeRoutineHouseViewActorDTO struct {
	ID                    string       `json:"id"`
	RoomID                string       `json:"room_id"`
	Position              [3]float64   `json:"position"`
	ActiveKind            string       `json:"active_kind,omitempty"`
	ActivePose            string       `json:"active_pose,omitempty"`
	ActivityStartedAt     string       `json:"activity_started_at,omitempty"`
	ActivityDueAt         string       `json:"activity_due_at,omitempty"`
	MovePoints            [][3]float64 `json:"move_points,omitempty"`
	TargetRoomID          string       `json:"target_room_id,omitempty"`
	MirrorPoseCompletions *uint64      `json:"mirror_pose_completions,omitempty"`
}

func registerRuntimeRoutineHouseViewRoute(mux *http.ServeMux, cfg *config.Config, deps *Dependencies) {
	if mux == nil {
		return
	}
	mux.HandleFunc(runtimeRoutineHouseViewPath, runtimeRoutineHouseViewHandler(cfg, deps))
}

func runtimeRoutineHouseViewHandler(cfg *config.Config, deps *Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r == nil {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != runtimeRoutineHouseViewPath || r.URL.EscapedPath() != runtimeRoutineHouseViewPath {
			http.NotFound(w, r)
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "query parameters are not supported", http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-RenCrow-Client") != "RenCrow_PORTAL" || r.Header.Get(interactionProfileHeader) != "portal-living" {
			http.NotFound(w, r)
			return
		}
		if !security.IsDirectLoopbackRequest(r) {
			http.NotFound(w, r)
			return
		}
		if cfg == nil || !cfg.Heartbeat.Routine.Enabled {
			http.NotFound(w, r)
			return
		}
		if deps == nil || deps.agentRoutine == nil || deps.agentRoutine.world == nil || !runtimeRoutineHouseViewHasCompleteActorScope(deps.agentRoutine) {
			http.Error(w, "WORLD home view unavailable", http.StatusServiceUnavailable)
			return
		}

		read, err := deps.agentRoutine.readSnapshotFresh(r.Context())
		if err != nil {
			http.Error(w, "WORLD home view unavailable", http.StatusServiceUnavailable)
			return
		}
		response, err := runtimeRoutineHouseViewProject(deps.agentRoutine, read)
		if err != nil {
			http.Error(w, "WORLD home view unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}
}

func runtimeRoutineHouseViewHasCompleteActorScope(runtime *agentRoutineRuntime) bool {
	if runtime == nil || len(runtime.worldActorIDs) != len(agentHeartbeatActorIDs) {
		return false
	}
	seen := make(map[string]struct{}, len(runtime.worldActorIDs))
	for _, coreActorID := range runtime.worldActorIDs {
		if !isAgentHeartbeatActor(coreActorID) {
			return false
		}
		if _, duplicate := seen[coreActorID]; duplicate {
			return false
		}
		seen[coreActorID] = struct{}{}
	}
	for _, coreActorID := range agentHeartbeatActorIDs {
		if _, ok := seen[coreActorID]; !ok {
			return false
		}
	}
	return true
}

func runtimeRoutineHouseViewProject(runtime *agentRoutineRuntime, read routineSnapshotRead) (runtimeRoutineHouseViewResponse, error) {
	if runtime == nil || !runtimeRoutineHouseViewHasCompleteActorScope(runtime) || read.snapshot.WorldID == "" || read.snapshot.HomeSceneSHA256 == "" {
		return runtimeRoutineHouseViewResponse{}, errRoutineHouseViewSnapshotUnavailable
	}
	actorByCoreID := make(map[string]runtimeRoutineHouseViewActorDTO, len(read.snapshot.Actors))
	for _, actor := range read.snapshot.Actors {
		coreActorID, allowed := runtime.worldActorIDs[actor.ActorID]
		if !allowed {
			continue
		}
		if !actor.Present || !actor.HasPosition || actor.RoomID == "" {
			return runtimeRoutineHouseViewResponse{}, errRoutineHouseViewSnapshotUnavailable
		}
		if actor.HomeSceneSHA256 != "" && actor.HomeSceneSHA256 != read.snapshot.HomeSceneSHA256 {
			return runtimeRoutineHouseViewResponse{}, errRoutineHouseViewSnapshotUnavailable
		}
		dto := runtimeRoutineHouseViewActorDTO{ID: coreActorID, RoomID: actor.RoomID, Position: actor.Position}
		if coreActorID == "kuro" {
			completions := actor.MirrorPoseCompletions
			dto.MirrorPoseCompletions = &completions
		}
		if actor.ActivityID != "" {
			switch actor.ActivityKind {
			case "move", "sleep", "exercise", "mirror_pose":
				dto.ActiveKind = actor.ActivityKind
				if !actor.ActivityStartedAt.IsZero() {
					dto.ActivityStartedAt = actor.ActivityStartedAt.UTC().Format(time.RFC3339Nano)
				}
				if !actor.ActivityDueAt.IsZero() {
					dto.ActivityDueAt = actor.ActivityDueAt.UTC().Format(time.RFC3339Nano)
				}
				if actor.ActivityKind == "move" {
					dto.TargetRoomID = actor.TargetRoomID
					if points := read.snapshot.MovePoints[actor.ActorID]; len(points) > 0 {
						dto.MovePoints = append([][3]float64(nil), points...)
					}
				}
				if actor.ActivityKind == "mirror_pose" {
					if routineMirrorPoseID(actor.PoseID) {
						dto.ActivePose = actor.PoseID
					}
				}
			}
		}
		actorByCoreID[coreActorID] = dto
	}
	actorIDs := make([]string, 0, len(actorByCoreID))
	for _, coreActorID := range agentHeartbeatActorIDs {
		if _, ok := actorByCoreID[coreActorID]; !ok {
			return runtimeRoutineHouseViewResponse{}, errRoutineHouseViewSnapshotUnavailable
		}
		actorIDs = append(actorIDs, coreActorID)
	}
	sort.Strings(actorIDs)
	actors := make([]runtimeRoutineHouseViewActorDTO, 0, len(actorIDs))
	for _, coreActorID := range actorIDs {
		actors = append(actors, actorByCoreID[coreActorID])
	}

	observedAt := read.readAt
	if observedAt.IsZero() {
		return runtimeRoutineHouseViewResponse{}, errRoutineHouseViewSnapshotUnavailable
	}
	return runtimeRoutineHouseViewResponse{
		SceneSHA256: read.snapshot.HomeSceneSHA256,
		StateSeq:    read.snapshot.StateSeq,
		ObservedAt:  observedAt.UTC().Format(time.RFC3339Nano),
		Actors:      actors,
	}, nil
}

func routineMirrorPoseID(poseID string) bool {
	for _, allowed := range routineMirrorPoseIDs {
		if poseID == allowed {
			return true
		}
	}
	return false
}
