package worker

// LogEntry is one line of job output as stored in the object store and
// returned by the log endpoints. Workers ship these as JSON arrays under
// logs/{job_id}/{stream}.json.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Stream    string `json:"stream"`
	Level     string `json:"level,omitempty"`
	Message   string `json:"message"`
}
