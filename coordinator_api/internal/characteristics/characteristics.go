package characteristics

// Characteristics is a set of key/value characteristics. Keys are non-empty
// strings. Job/queue Characteristics hold only scalar Values (StringValue,
// IntValue, BoolValue); worker Characteristics may also hold list Values
// (StringListValue, IntListValue, BoolListValue). Construct instances via
// ParseJobCharacteristics or ParseWorkerCharacteristics rather than building
// the map directly, so validation (and the job "os" default) is applied
// consistently.
type Characteristics map[string]Value
