package viewer

// EmotionType represents character emotions
type EmotionType string

const (
	EmotionNormal   EmotionType = "normal"
	EmotionHappy    EmotionType = "happy"
	EmotionSad      EmotionType = "sad"
	EmotionAngry    EmotionType = "angry"
	EmotionSurprise EmotionType = "surprise"
	EmotionThink    EmotionType = "think"
	EmotionSpeaking EmotionType = "speaking"
)
