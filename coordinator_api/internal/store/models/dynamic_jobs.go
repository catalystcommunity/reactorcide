package models

// Dynamic jobs settings. A dynamic job is one a running job creates at
// runtime (runnerlib trigger, trigger_job()), not one the event's eval
// declared from workflow files.
const (
	DynamicJobsInherit = "inherit"
	DynamicJobsAllow   = "allow"
	DynamicJobsDeny    = "deny"
)

// EffectiveDynamicJobs resolves a project setting against its organization
// setting. Anything but an explicit "deny" allows, so a missing or unknown
// value keeps the pre-setting behavior.
func EffectiveDynamicJobs(projectSetting, orgSetting string) string {
	switch projectSetting {
	case DynamicJobsAllow, DynamicJobsDeny:
		return projectSetting
	}
	if orgSetting == DynamicJobsDeny {
		return DynamicJobsDeny
	}
	return DynamicJobsAllow
}

// IsValidDynamicJobsOrgSetting reports whether value may be stored on an
// organization.
func IsValidDynamicJobsOrgSetting(value string) bool {
	return value == DynamicJobsAllow || value == DynamicJobsDeny
}

// IsValidDynamicJobsProjectSetting reports whether value may be stored on a
// project.
func IsValidDynamicJobsProjectSetting(value string) bool {
	return value == DynamicJobsInherit || IsValidDynamicJobsOrgSetting(value)
}
