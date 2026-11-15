package vcs

// EventSchedule marks an eval started by a coordinator schedule, not by a VCS
// webhook. The eval runs one workflow (the schedule's workflow_ref) on the
// branch head.
const EventSchedule EventType = "schedule"
