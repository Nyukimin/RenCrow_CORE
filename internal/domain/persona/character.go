package persona

type CharacterProfile struct {
	CharacterID string            `json:"character_id"`
	Lore        map[string]string `json:"lore"`
	Persona     map[string]string `json:"persona"`
	Modes       map[string]string `json:"modes"`
}
