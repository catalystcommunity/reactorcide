package characteristics

// Satisfies reports whether worker W satisfies queue Q: the ONLY matching
// rule for Wave 1 (WORKERS_PLAN.md, "Satisfies predicate"). For every (k, v)
// in Q, W must have key k, and either W[k] equals v (when W[k] is scalar) or
// v is a member of W[k] (when W[k] is a list) — with strict type equality in
// both cases. W may carry extra keys Q does not name; that never disqualifies
// it. Q's values are always scalar (ParseJobCharacteristics enforces this at
// parse time), but Satisfies does not itself require that — it simply calls
// Value.Matches, which is defined against a scalar argument.
func Satisfies(worker, queue Characteristics) bool {
	for k, qv := range queue {
		wv, ok := worker[k]
		if !ok {
			return false
		}
		if !wv.Matches(qv) {
			return false
		}
	}
	return true
}
