package profilegrant

// ValidIdentity applies the grant identity allowlist to v, so stores and hooks
// that put identity-ish values in filenames or state use the same rule the
// canonical text does.
func ValidIdentity(what, v string) error { return checkIdentityValue(what, v) }
