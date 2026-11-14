package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
)

var errRoutineGameFactScope = errors.New("routine game fact scope is invalid")

func (r *agentRoutineRuntime) collectRoutineGameFacts(ctx context.Context, actorID, date string) ([]agentroutine.Activity, error) {
	if ctx == nil || r == nil || !routineFactActorID(actorID) {
		return nil, errRoutineGameFactScope
	}
	dayStart, dayEnd, err := routineFactDay(date)
	if err != nil {
		return nil, errRoutineGameFactScope
	}
	if r.deps == nil || r.deps.gameBridgeStore == nil {
		return []agentroutine.Activity{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	facts, err := r.deps.gameBridgeStore.PlayedGameSessionFacts(ctx, actorID, dayStart, dayEnd)
	if err != nil {
		return nil, fmt.Errorf("list canonical GAMES routine facts: %w", err)
	}
	activities := make([]agentroutine.Activity, 0, len(facts))
	for _, fact := range facts {
		gameTitle, ok := routineGameTitle(fact.GameID)
		if !ok || fact.SessionID == "" || fact.TurnCount <= 0 || fact.FirstAt.Before(dayStart) || !fact.FirstAt.Before(dayEnd) || fact.LastAt.Before(fact.FirstAt) || !fact.LastAt.Before(dayEnd) {
			return nil, errors.New("canonical GAMES routine fact violates its scope")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sourceID := fact.GameID + ":" + fact.SessionID
		firstLocal := fact.FirstAt.In(dayStart.Location()).Format("15:04")
		lastLocal := fact.LastAt.In(dayStart.Location()).Format("15:04")
		activities = append(activities, agentroutine.Activity{
			ActorID:   actorID,
			Date:      date,
			Key:       "game-session:" + sourceID,
			Kind:      agentroutine.ActivityHobby,
			At:        fact.LastAt.UTC(),
			Source:    "canonical_game_session",
			SourceID:  sourceID,
			Confirmed: true,
			Summary:   fmt.Sprintf("GAMESで%sを%dターン遊んだ（初回 %s、最終 %s JST）", gameTitle, fact.TurnCount, firstLocal, lastLocal),
		})
	}
	return activities, nil
}

func routineGameTitle(gameID string) (string, bool) {
	switch gameID {
	case "nethack":
		return "NetHack", true
	case "survival_garden":
		return "サバイバルガーデン", true
	case "territory_commander":
		return "テリトリーコマンダー", true
	case "herzog_zwei":
		return "ヘルツォーク・ツヴァイ", true
	default:
		return "", false
	}
}
