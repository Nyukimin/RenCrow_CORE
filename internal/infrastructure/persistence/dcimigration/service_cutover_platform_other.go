//go:build !linux

package dcimigration

// newPlatformCutoverServiceManager deliberately has no substitute on
// non-Linux hosts.  The public API remains portable, but mutation is rejected
// before the private lifecycle owner can be invoked.
func newPlatformCutoverServiceManager(_, _ string) (cutoverServiceManager, error) {
	return nil, newCodedError("service_manager_unavailable", "canonical service manager is unavailable")
}
