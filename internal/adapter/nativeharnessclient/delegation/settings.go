package delegation

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"
)

const (
	// ClientName and ClientVersion identify CORE in the initialize handshake.
	// They are diagnostics, not an authentication: the connection principal is
	// fixed by the Harness config the child is started with.
	ClientName    = "rencrow-core"
	ClientVersion = "1"

	// upstreamOwner is the owner of every CORE ID placed in the Harness upstream.
	upstreamOwner = "RenCrow_CORE"
	// shiroAgentID is the agent of the profile: the Agent identity and the
	// execution responsibility stay with Shiro.
	shiroAgentID = "shiro"

	defaultStartTimeout    = 30 * time.Second
	defaultStepTimeout     = 30 * time.Second
	defaultRestartCooldown = 10 * time.Second
	defaultCloseDeadline   = 10
	maxIdentifierRunes     = 128
)

// Settings are the authenticated settings of the profile, built from the
// native_harness section of the CORE configuration (the configuration owns the
// validation; the checks here only keep this package fail-closed on its own).
type Settings struct {
	// HarnessBinary and HarnessConfig are absolute paths. The child is started as
	// `HarnessBinary serve --stdio --config HarnessConfig`, without a shell, with
	// an empty environment and with the directory of HarnessConfig as working
	// directory.
	HarnessBinary string
	HarnessConfig string
	// ExpectedBuildRevision is the build_revision the Harness must report.
	ExpectedBuildRevision string

	// Workspace is the root, policy and mode of the Harness session.
	Workspace Workspace
	// Binding is the existing Shiro Gateway binding (an execution alias). The
	// agent is always shiro.
	Binding Binding
	// Limits are the complete limits sent with every start.
	Limits protocol.Limits

	// StartTimeout bounds starting the child and the initialize handshake.
	StartTimeout time.Duration
	// StepTimeout bounds the session/open and turn/start calls of one delegation
	// (they are not cut short by the cancellation of the turn, so a cancellation
	// cannot leave a Run of unknown outcome behind).
	StepTimeout time.Duration
	// RestartCooldown is how long after a failed start the next admission is
	// blocked at once, so a Harness that cannot start is not started again for
	// every turn.
	RestartCooldown time.Duration
	// CancelGrace is how long a stop signal waits for the Run to end. Zero means
	// the client default.
	CancelGrace time.Duration
}

// Workspace is workspace_ref of the 05 profile table, resolved.
type Workspace struct {
	Path          string
	PolicyRef     string
	ExecutionMode string
}

// Binding is binding_ref of the 05 profile table, resolved.
type Binding struct {
	Selector        string
	ProfileRevision string
	ExecutionRole   string
}

// ErrInvalidSettings reports settings this package cannot run with.
var ErrInvalidSettings = errors.New("invalid native harness delegation settings")

func (s Settings) withDefaults() Settings {
	if s.StartTimeout <= 0 {
		s.StartTimeout = defaultStartTimeout
	}
	if s.StepTimeout <= 0 {
		s.StepTimeout = defaultStepTimeout
	}
	if s.RestartCooldown < 0 {
		s.RestartCooldown = 0
	} else if s.RestartCooldown == 0 {
		s.RestartCooldown = defaultRestartCooldown
	}
	return s
}

func (s Settings) validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidSettings, fmt.Sprintf(format, args...))
	}
	for _, path := range []struct{ name, value string }{
		{"harness binary", s.HarnessBinary}, {"harness config", s.HarnessConfig}, {"workspace path", s.Workspace.Path},
	} {
		if path.value == "" || !filepath.IsAbs(path.value) || strings.ContainsRune(path.value, 0) {
			return bad("%s must be an absolute path", path.name)
		}
	}
	for _, text := range []struct{ name, value string }{
		{"expected build revision", s.ExpectedBuildRevision},
		{"policy ref", s.Workspace.PolicyRef},
		{"binding selector", s.Binding.Selector},
		{"binding profile revision", s.Binding.ProfileRevision},
		{"binding execution role", s.Binding.ExecutionRole},
	} {
		if !plainIdentifier(text.value) {
			return bad("%s must be 1..%d characters of text without control characters", text.name, maxIdentifierRunes)
		}
	}
	switch s.Workspace.ExecutionMode {
	case protocol.ModeStructuredOnly, protocol.ModeTrustedHost:
	default:
		return bad("execution mode must be %s or %s", protocol.ModeStructuredOnly, protocol.ModeTrustedHost)
	}
	return nil
}

func plainIdentifier(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxIdentifierRunes {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// requiredCapabilities are what CORE needs ready after initialize: the client
// defaults (open a Thread, admit and execute a Run on a model, stop it, report
// on it) plus the Tool runtime of the configured execution mode. A Harness that
// cannot supply them is not used, and the work is not run elsewhere.
func (s Settings) requiredCapabilities() []string {
	return append(clientDefaultCapabilities(), "tool.runtime", "tool.runtime."+s.Workspace.ExecutionMode)
}
