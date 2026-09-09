//go:build !linux

package runmigration

func newRunCutoverService(string, string) (runCutoverService, error) {
	return nil, runCutoverFailure{"service_owner_unavailable"}
}
