package store

// APITokenListFilter selects one page of API tokens.
type APITokenListFilter struct {
	// AllOrganizations lists every token. Otherwise only tokens that an
	// organization in OrgIDs owns, or that have one of OrgIDs in their
	// scope, are listed (none when OrgIDs is empty).
	AllOrganizations bool
	OrgIDs           []string
	// ExcludeExpired leaves out tokens whose expires_at is in the past.
	// Tokens with no expiry are always listed.
	ExcludeExpired bool
	// Limit is the page size (0 means no limit). Offset is the number of
	// matching tokens to skip.
	Limit  int
	Offset int
}
