package dcimigration

// ReadValidatedCutoverReceipt exposes the canonical strict reader to the
// CORE-owned operational verifier without duplicating migration validation.
func ReadValidatedCutoverReceipt(path string) (CutoverReceipt, error) {
	return readCutoverReceipt(path)
}

// ReadValidatedServiceCutoverReceipt exposes the canonical strict reader to
// the CORE-owned operational verifier without creating a second receipt canon.
func ReadValidatedServiceCutoverReceipt(path string) (ServiceCutoverReceipt, error) {
	return readServiceCutoverReceipt(path)
}
