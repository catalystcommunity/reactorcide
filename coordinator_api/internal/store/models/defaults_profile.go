package models

// Defaults profiles (plan item A5, owner decision D15). The profile records
// which defaults an organization uses. The stored settings are the authority:
// a profile only writes them, and an administrator can change each one later.
//
//   - secure: run_gate outside_contributors, dynamic_jobs deny, the
//     built-in CI policy for a project with no stored CI policy, and
//     cache_write_classes [default, tag] on the "standard" execution
//     profile (a pull request job does not write a cache). New
//     organizations get this profile.
//   - legacy: the behavior before secure defaults. A project with no stored
//     policy runs the CI of a same-repository pull request head. Migration
//     000055 gives this profile to every organization that existed before.
const (
	DefaultsProfileLegacy = "legacy"
	DefaultsProfileSecure = "secure"
)

// IsValidDefaultsProfile reports whether value is a known profile.
func IsValidDefaultsProfile(value string) bool {
	return value == DefaultsProfileLegacy || value == DefaultsProfileSecure
}

// DefaultsProfileSettings are the settings that a profile writes.
type DefaultsProfileSettings struct {
	// RunGate is the organization run_gate.
	RunGate []string
	// DynamicJobs is the organization dynamic_jobs value.
	DynamicJobs string
	// BuiltInPolicy is true when a project with no stored CI policy uses
	// the built-in policy (CI from base) for pull requests.
	BuiltInPolicy bool
	// CacheWriteClasses is the cache_write_classes value of the "standard"
	// execution profile of the organization. Empty permits writes for
	// every ref class (the behavior before).
	CacheWriteClasses []string
}

// SettingsForDefaultsProfile returns the settings of a profile. An unknown
// profile returns the legacy settings.
func SettingsForDefaultsProfile(profile string) DefaultsProfileSettings {
	if profile == DefaultsProfileSecure {
		return DefaultsProfileSettings{
			RunGate:       []string{RunGateOutsideContributors},
			DynamicJobs:   DynamicJobsDeny,
			BuiltInPolicy: true,
			// Classes "default" and "tag" (refclass.ClassDefault, ClassTag).
			CacheWriteClasses: []string{"default", "tag"},
		}
	}
	return DefaultsProfileSettings{RunGate: []string{}, DynamicJobs: DynamicJobsAllow, CacheWriteClasses: []string{}}
}

// UsesBuiltInPolicy reports whether an organization with this profile gives
// a project with no stored CI policy the built-in policy.
func UsesBuiltInPolicy(profile string) bool {
	return SettingsForDefaultsProfile(profile).BuiltInPolicy
}
