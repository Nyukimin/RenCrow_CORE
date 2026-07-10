package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Live2DEmotionMapping maps emotion types to Live2D motions/expressions
var Live2DEmotionMapping = map[EmotionType]Live2DState{
	EmotionNormal: {
		Motion:     "",
		Expression: "f01", // Normal face
	},
	EmotionHappy: {
		Motion:     "tapBody",
		Expression: "f02", // Happy/Smile
	},
	EmotionSad: {
		Motion:     "",
		Expression: "f03", // Sad
	},
	EmotionAngry: {
		Motion:     "shake",
		Expression: "f04", // Angry
	},
	EmotionSurprise: {
		Motion:     "pinchIn",
		Expression: "f05", // Surprised
	},
	EmotionThink: {
		Motion:     "",
		Expression: "f06", // Thinking
	},
	EmotionSpeaking: {
		Motion:     "tapBody",
		Expression: "f02", // Happy while speaking
	},
}

// Live2DState represents the state of Live2D character
type Live2DState struct {
	Motion     string  `json:"motion"`         // Motion name (e.g., "tapBody", "shake")
	Expression string  `json:"expression"`     // Expression ID (e.g., "f01", "f02")
	Scale      float64 `json:"scale,omitzero"` // Scale (optional)
	X          float64 `json:"x,omitzero"`     // X position (optional)
	Y          float64 `json:"y,omitzero"`     // Y position (optional)
}

// Live2DControlMessage represents a control message sent to Live2D iframe
type Live2DControlMessage struct {
	Type    string      `json:"type"` // "emotion", "motion", "expression"
	Emotion EmotionType `json:"emotion,omitempty"`
	State   Live2DState `json:"state,omitempty"`
}

// HandleLive2DEmotionControl handles emotion control requests
func HandleLive2DEmotionControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Emotion EmotionType `json:"emotion"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	state, ok := Live2DEmotionMapping[req.Emotion]
	if !ok {
		state = Live2DEmotionMapping[EmotionNormal]
	}

	msg := Live2DControlMessage{
		Type:    "emotion",
		Emotion: req.Emotion,
		State:   state,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(msg)
}

// BuildLive2DControlScript generates JavaScript for controlling Live2D
func BuildLive2DControlScript(emotion EmotionType) string {
	state, ok := Live2DEmotionMapping[emotion]
	if !ok {
		state = Live2DEmotionMapping[EmotionNormal]
	}

	return fmt.Sprintf(`
<script>
(function() {
	var currentEmotion = '%s';
	var live2dState = %s;

	// Wait for Live2D to load
	function waitForLive2D(callback) {
		if (window.live2d && window.live2d.model) {
			callback();
		} else {
			setTimeout(function() { waitForLive2D(callback); }, 100);
		}
	}

	// Set emotion
	function setEmotion(emotion, state) {
		console.log('Setting Live2D emotion:', emotion, state);

		if (!window.live2d || !window.live2d.model) {
			console.warn('Live2D not ready');
			return;
		}

		try {
			// Set expression
			if (state.expression && window.live2d.model.setExpression) {
				window.live2d.model.setExpression(state.expression);
			}

			// Start motion
			if (state.motion && window.live2d.model.startMotion) {
				window.live2d.model.startMotion('tapBody', state.motion, 3);
			}
		} catch (e) {
			console.error('Live2D control error:', e);
		}
	}

	// Listen for messages from parent
	window.addEventListener('message', function(event) {
		if (event.data.type === 'emotion') {
			console.log('Received emotion:', event.data);
			setEmotion(event.data.emotion, event.data.state);
		}
	});

	// Initial emotion on load
	waitForLive2D(function() {
		setEmotion(currentEmotion, live2dState);
	});

	// Expose API
	window.live2dController = {
		setEmotion: setEmotion
	};
})();
</script>
`, emotion, toJSON(state))
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
