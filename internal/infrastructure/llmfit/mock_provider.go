package llmfit

import (
	"context"
	"sync"

	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
)

// MockProvider is a CapabilityProvider for tests. It returns fixed values or
// fixed errors and counts calls. It lives in a non-test file only so tests in
// other packages (application, viewer) can reuse it; production code must not
// reference it.
type MockProvider struct {
	mu sync.Mutex

	HealthErr     error
	Profile       *domainllmops.NodeHardwareProfile
	SystemErr     error
	Models        []domainllmops.ModelFitAssessment
	ModelsErr     error
	SearchResults []domainllmops.ModelFitAssessment
	SearchErr     error

	HealthCalls int
	SystemCalls int
	TopCalls    int
	SearchCalls int
	LastQuery   ModelFitQuery
}

// Health implements CapabilityProvider.
func (m *MockProvider) Health(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.HealthCalls++
	return m.HealthErr
}

// System implements CapabilityProvider.
func (m *MockProvider) System(ctx context.Context) (*domainllmops.NodeHardwareProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SystemCalls++
	if m.SystemErr != nil {
		return nil, m.SystemErr
	}
	if m.Profile == nil {
		return nil, newProviderError(ErrorKindInvalidResponse, "", "system", "mock has no profile", nil)
	}
	profile := *m.Profile
	profile.GPUs = append([]domainllmops.GPUProfile(nil), m.Profile.GPUs...)
	return &profile, nil
}

// TopModels implements CapabilityProvider.
func (m *MockProvider) TopModels(ctx context.Context, query ModelFitQuery) ([]domainllmops.ModelFitAssessment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TopCalls++
	m.LastQuery = query
	if m.ModelsErr != nil {
		return nil, m.ModelsErr
	}
	return append([]domainllmops.ModelFitAssessment(nil), m.Models...), nil
}

// SearchModels implements CapabilityProvider.
func (m *MockProvider) SearchModels(ctx context.Context, query ModelFitQuery) ([]domainllmops.ModelFitAssessment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SearchCalls++
	m.LastQuery = query
	if m.SearchErr != nil {
		return nil, m.SearchErr
	}
	return append([]domainllmops.ModelFitAssessment(nil), m.SearchResults...), nil
}

// Calls returns a snapshot of the call counters.
func (m *MockProvider) Calls() (health, system, top, search int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.HealthCalls, m.SystemCalls, m.TopCalls, m.SearchCalls
}

// Set replaces the fixed responses under the mutex (for mid-test changes).
func (m *MockProvider) Set(fn func(m *MockProvider)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m)
}

var _ CapabilityProvider = (*MockProvider)(nil)
var _ CapabilityProvider = (*HTTPProvider)(nil)
var _ CapabilityProvider = (*CLIProvider)(nil)
