package types

// CelestiaClone returns a new Any equal to a freshly decoded one: TypeUrl,
// copies of Value and XXX_unrecognized, and the same cached (unpacked) value.
// The cached value is shared: unpacked interface values (e.g. public keys) are
// treated as immutable by the SDK. The amino compat state is not carried over
// (a freshly decoded Any has none) and XXX_sizecache starts at 0, as after a
// decode. nil stays nil.
//
// Used by the env-gated decoded-validator cache (CELESTIA_VALIDATOR_CACHE).
func (any *Any) CelestiaClone() *Any {
	if any == nil {
		return nil
	}
	c := &Any{
		TypeUrl:     any.TypeUrl,
		cachedValue: any.cachedValue,
	}
	if any.Value != nil {
		c.Value = append(make([]byte, 0, len(any.Value)), any.Value...)
	}
	if any.XXX_unrecognized != nil {
		c.XXX_unrecognized = append(make([]byte, 0, len(any.XXX_unrecognized)), any.XXX_unrecognized...)
	}
	return c
}
