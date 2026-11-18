package taskcenter

// NativeExecutionAvailable reports whether the typed native executor has all
// of its required owner ports connected. Runtime capability text uses this
// instead of advertising tasks.execute from a method-shaped stub.
func (s *Service) NativeExecutionAvailable() bool {
	return s != nil && s.nativeExecution != nil && s.nativeExecution.Tasks != nil &&
		s.nativeExecution.Results != nil && s.nativeExecution.Movies != nil
}
