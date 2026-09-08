package idlechat

import "slices"

// cloneDialogueEpisode isolates the durable checkpoint from mutable generation
// and validation state, including topic evidence and the dialogue plan.
func cloneDialogueEpisode(a DialogueEpisodeArtifact) DialogueEpisodeArtifact {
	a.Participants = slices.Clone(a.Participants)
	a.Turns = slices.Clone(a.Turns)
	a.Validation.Errors = slices.Clone(a.Validation.Errors)
	for i := range a.Validation.Errors {
		a.Validation.Errors[i].Reasons = slices.Clone(a.Validation.Errors[i].Reasons)
	}
	r := &a.TopicResult
	r.Candidates = slices.Clone(r.Candidates)
	if r.Judge != nil {
		judge := *r.Judge
		judge.Scores = slices.Clone(judge.Scores)
		r.Judge = &judge
	}
	r.Seed.TrendKeywords = slices.Clone(r.Seed.TrendKeywords)
	r.Seed.RecentTopics = slices.Clone(r.Seed.RecentTopics)
	if r.Seed.News != nil {
		news := *r.Seed.News
		news.TermNotes = slices.Clone(news.TermNotes)
		r.Seed.News = &news
	}
	if r.Seed.ExternalMaterial != nil {
		material := *r.Seed.ExternalMaterial
		r.Seed.ExternalMaterial = &material
	}
	p := &a.ArcPlan
	p.ContentModeReasons = slices.Clone(p.ContentModeReasons)
	p.DevelopmentMoves = slices.Clone(p.DevelopmentMoves)
	p.DeepeningMoves = slices.Clone(p.DeepeningMoves)
	p.ForbiddenMoves = slices.Clone(p.ForbiddenMoves)
	if p.SpeakerRoles != nil {
		roles := make(map[string]DialogueSpeakerRole, len(p.SpeakerRoles))
		for key, role := range p.SpeakerRoles {
			role.Avoid = slices.Clone(role.Avoid)
			roles[key] = role
		}
		p.SpeakerRoles = roles
	}
	p.TurnPlans = slices.Clone(p.TurnPlans)
	for i := range p.TurnPlans {
		p.TurnPlans[i].Avoid = slices.Clone(p.TurnPlans[i].Avoid)
	}
	return a
}
