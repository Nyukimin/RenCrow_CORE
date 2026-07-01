package conversation

import "time"

// UserProfile はユーザーの好み・傾向を管理
type UserProfile struct {
	UserID      string            `json:"user_id"`
	Preferences map[string]string `json:"preferences"`
	Facts       []string          `json:"facts"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// NewUserProfile は空の UserProfile を作成
func NewUserProfile(userID string) UserProfile {
	return UserProfile{
		UserID:      userID,
		Preferences: make(map[string]string),
		Facts:       []string{},
		UpdatedAt:   time.Now(),
	}
}

// Merge は新しいプロファイル情報をマージする
func (p *UserProfile) Merge(newPrefs map[string]string, newFacts []string) {
	for k, v := range newPrefs {
		p.Preferences[k] = v
	}
	existing := make(map[string]bool)
	for _, f := range p.Facts {
		existing[f] = true
	}
	for _, f := range newFacts {
		if !existing[f] {
			p.Facts = append(p.Facts, f)
		}
	}
	p.UpdatedAt = time.Now()
}

// ToPromptText はプロンプト注入用のテキストを返す
func (p *UserProfile) ToPromptText() string {
	if len(p.Preferences) == 0 && len(p.Facts) == 0 {
		return ""
	}
	text := "【ユーザーについて知っていること】\n"
	for k, v := range p.Preferences {
		text += "- " + k + ": " + v + "\n"
	}
	for _, f := range p.Facts {
		text += "- " + f + "\n"
	}
	return text
}
