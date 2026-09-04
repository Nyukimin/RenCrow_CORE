package archivesqlite

import modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

// archiveTestThreadID gives fixtures stable canonical ThreadIDs without using
// runtime randomness or the retired numeric representation.
func archiveTestThreadID(seed string) modulecore.ThreadID {
	raw, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "archivesqlite_test", "thread_id", seed)
	if err != nil {
		panic(err)
	}
	return modulecore.ThreadID(raw)
}

func archiveTestSessionID(seed string) string {
	raw, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "archivesqlite_test", "session_id", seed)
	if err != nil {
		panic(err)
	}
	return raw
}
