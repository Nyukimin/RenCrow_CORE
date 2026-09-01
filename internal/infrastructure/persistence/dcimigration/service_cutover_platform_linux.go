//go:build linux

package dcimigration

// newPlatformCutoverServiceManager is the fixed Linux owner adapter.  The
// platform-specific implementation keeps the service unit, command, port,
// readiness endpoint, and lifecycle order private in the existing systemd
// manager.
func newPlatformCutoverServiceManager(installedRuntime, activeConfig string) (cutoverServiceManager, error) {
	return newLinuxSystemdCutoverServiceManager(installedRuntime, activeConfig)
}
