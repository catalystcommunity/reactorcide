package uiapi

import (
	"sort"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// sortedCharacteristicKeys returns c's keys sorted, so every wire conversion
// below produces a deterministic entry order (map iteration order is not
// stable) — purely for stable test/UI ordering, not a hashing/matching
// concern (characteristics.Hash never runs over the wire form).
func sortedCharacteristicKeys(c characteristics.Characteristics) []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// queueCharacteristicsToCsil renders c (queue/job characteristics, always
// scalar by construction — see characteristics.ParseJobCharacteristics) as
// the wire CharacteristicEntry list the admin queue ops return.
func queueCharacteristicsToCsil(c characteristics.Characteristics) []csilapi.CharacteristicEntry {
	keys := sortedCharacteristicKeys(c)
	out := make([]csilapi.CharacteristicEntry, 0, len(keys))
	for _, k := range keys {
		if v, ok := scalarToWire(c[k]); ok {
			out = append(out, csilapi.CharacteristicEntry{Key: k, Value: v})
		}
	}
	return out
}

// workerCharacteristicsToCsil renders c (worker characteristics, which may
// hold homogeneous lists — see characteristics.ParseWorkerCharacteristics)
// as the wire WorkerCharacteristicEntry list the admin list-workers op
// returns.
func workerCharacteristicsToCsil(c characteristics.Characteristics) []csilapi.WorkerCharacteristicEntry {
	keys := sortedCharacteristicKeys(c)
	out := make([]csilapi.WorkerCharacteristicEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, csilapi.WorkerCharacteristicEntry{Key: k, Value: valueToWire(c[k])})
	}
	return out
}

// scalarToWire converts a scalar characteristics.Value to its wire
// representation; ok is false for a (should-never-happen, per
// ParseJobCharacteristics' scalar-only contract) list value, so a caller can
// skip rather than send a nonsensical entry.
func scalarToWire(v characteristics.Value) (any, bool) {
	switch t := v.(type) {
	case characteristics.StringValue:
		return string(t), true
	case characteristics.IntValue:
		return int64(t), true
	case characteristics.BoolValue:
		return bool(t), true
	default:
		return nil, false
	}
}

// valueToWire converts any characteristics.Value (scalar or list) to its
// wire representation, for worker characteristics.
func valueToWire(v characteristics.Value) any {
	switch t := v.(type) {
	case characteristics.StringValue:
		return string(t)
	case characteristics.IntValue:
		return int64(t)
	case characteristics.BoolValue:
		return bool(t)
	case characteristics.StringListValue:
		return []string(t)
	case characteristics.IntListValue:
		return []int64(t)
	case characteristics.BoolListValue:
		return []bool(t)
	default:
		return nil
	}
}

// csilCharacteristicsToRaw converts a wire CharacteristicEntry list (as
// decoded off the envelope — values are bare string/int64/bool per
// CharacteristicScalar's union) into the map[string]any
// characteristics.ParseJobCharacteristics consumes for create-queue.
func csilCharacteristicsToRaw(entries []csilapi.CharacteristicEntry) map[string]any {
	raw := make(map[string]any, len(entries))
	for _, e := range entries {
		raw[e.Key] = e.Value
	}
	return raw
}
