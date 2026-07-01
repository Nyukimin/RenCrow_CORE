//go:build ignore

package idlechat

type StorySpec struct {
	Skeleton StorySkeleton
	Twists   map[string]string
}

func storySpecForSource(source StorySource) (StorySpec, bool) {
	spec, ok := storySpecs[source.ID]
	if !ok {
		return StorySpec{}, false
	}
	spec.Skeleton.ID = source.ID
	spec.Skeleton.SourceTitle = source.Title
	return spec, true
}
