package capability

// Profile is the logical work profile available through RenCrow_LLM.
type Profile string

const (
	ProfileCoderHigh     Profile = "coder-high"
	ProfileCoderStandard Profile = "coder-standard"
	ProfileWorker        Profile = "worker"
	ProfileUnavailable   Profile = "unavailable"
)

// DetermineProfile derives a node profile from available logical aliases.
func DetermineProfile(caps NodeCapabilities) Profile {
	maxQuality := 0
	for _, llm := range caps.LLMs {
		if llm.Available && llm.Quality > maxQuality {
			maxQuality = llm.Quality
		}
	}
	switch {
	case maxQuality >= 5:
		return ProfileCoderHigh
	case maxQuality >= 3:
		return ProfileCoderStandard
	case maxQuality >= 1:
		return ProfileWorker
	default:
		return ProfileUnavailable
	}
}
