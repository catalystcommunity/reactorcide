package outboundevents

import (
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The failure class filter of a diagnosis subscription (docs/diagnosis.md).
func TestMatchesFailureClasses(t *testing.T) {
	sub := baseSub()
	sub.FailureClasses = pq.StringArray{"job", "oom", "timeout"}
	ev := baseEvent()
	assert.True(t, Matches(sub, ev), "a job failure matches")

	ev.FailureClass = "infra.platform"
	assert.False(t, Matches(sub, ev), "an infrastructure failure does not match")

	ev.FailureClass = ""
	ev.Result, ev.Status = "completed", "completed"
	assert.False(t, Matches(sub, ev), "an event with no failure class does not match the filter")

	sub.FailureClasses = nil
	assert.True(t, Matches(sub, ev), "no filter matches every event")
}

func TestApplyFailureClassesValidates(t *testing.T) {
	sub := baseSub()
	classes := []string{"timeout", "job", "job", " oom "}
	require.NoError(t, applyFailureClasses(sub, &classes))
	assert.Equal(t, pq.StringArray{"job", "oom", "timeout"}, sub.FailureClasses)

	unknown := []string{"segfault"}
	err := applyFailureClasses(sub, &unknown)
	require.Error(t, err)
	assert.True(t, IsValidationError(err))

	require.NoError(t, applyFailureClasses(sub, nil))
	assert.Equal(t, pq.StringArray{"job", "oom", "timeout"}, sub.FailureClasses, "nil keeps the filter")
	empty := []string{}
	require.NoError(t, applyFailureClasses(sub, &empty))
	assert.Empty(t, sub.FailureClasses, "an empty list clears the filter")
}
