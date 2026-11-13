package main

import "io/fs"

// File ACLs on Windows are managed by the service owner.
func checkTokenPermissions(fs.FileMode) error { return nil }
