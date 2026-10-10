package store

// SetMaxEnvelopeMatches changes how many envelope matches a to: filter
// takes as a list, until the returned function restores it.
func SetMaxEnvelopeMatches(n int) (restore func()) {
	old := maxEnvelopeMatches
	maxEnvelopeMatches = n
	return func() { maxEnvelopeMatches = old }
}
