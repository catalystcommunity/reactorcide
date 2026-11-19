package cipolicy

// BuiltInDocument is the CI policy that a pull request of a project with no
// stored policy uses when its organization has the secure defaults profile
// (plan item A5). CI comes from the base. No head_ci rule exists, so the
// head's CI changes run only through /reactorcide override-ci, or after an
// operator stores a policy with a head_ci rule.
const BuiltInDocument = `{"defaults":{"ci_source":"base","profile":"standard"},"head_ci":[],"version":1}`

// BuiltIn returns the parsed built-in policy and its canonical document.
func BuiltIn() (*Policy, []byte, error) {
	policy, err := ParseDocument([]byte(BuiltInDocument))
	if err != nil {
		return nil, nil, err
	}
	canonical, err := CanonicalDocument(policy)
	if err != nil {
		return nil, nil, err
	}
	return policy, canonical, nil
}
