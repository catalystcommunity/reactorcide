package jobtelemetry

import (
	"reflect"
	"testing"
)

func labelsOf(pairs ...string) []Label {
	out := []Label{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Label{Key: pairs[i], Value: pairs[i+1]})
	}
	return out
}

// TestNormalizeSeriesLabelsIsTheInverseOfTheOldScheme is the back-compatibility
// guarantee. Telemetry stored before the scope label was removed must render
// exactly as if it had been written under the current scheme, or every job that
// ran before the change reads wrong.
func TestNormalizeSeriesLabelsIsTheInverseOfTheOldScheme(t *testing.T) {
	cases := []struct {
		name string
		in   []Label
		want []Label
		why  string
	}{
		{
			name: "docker job roll-up",
			in:   labelsOf("scope", "job", "component", "main"),
			want: []Label{},
			why:  "Docker's single container IS the job; component=main was noise on a roll-up",
		},
		{
			name: "kubernetes per-container",
			in:   labelsOf("scope", "component", "component", "builder"),
			want: labelsOf("component", "builder"),
			why:  "a real component keeps its label; only scope is redundant",
		},
		{
			name: "kubernetes job roll-up",
			in:   labelsOf("scope", "job", "cpu", "total"),
			want: labelsOf("cpu", "total"),
			why:  "scope goes, unrelated labels stay",
		},
		{
			name: "already current",
			in:   labelsOf("component", "docker"),
			want: labelsOf("component", "docker"),
			why:  "a series written under the new scheme passes through untouched",
		},
		{
			name: "no labels at all",
			in:   []Label{},
			want: []Label{},
			why:  "the new-scheme job roll-up has no labels",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeSeriesLabels(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("normalizeSeriesLabels(%v) = %v, want %v: %s", tc.in, got, tc.want, tc.why)
			}
		})
	}
}

// TestNormalizedOldAndNewDataAgree is the property that matters most: a job that
// ran under the OLD collectors and one that ran under the NEW collectors must
// produce identical labels for the same logical series.
func TestNormalizedOldAndNewDataAgree(t *testing.T) {
	oldDockerRollup := normalizeSeriesLabels(labelsOf("scope", "job", "component", "main"))
	newDockerRollup := normalizeSeriesLabels([]Label{})

	if SeriesComponent(oldDockerRollup) != SeriesComponent(newDockerRollup) {
		t.Errorf("an old Docker roll-up (%v) and a new one (%v) must both read as the job total",
			oldDockerRollup, newDockerRollup)
	}
	if SeriesComponent(oldDockerRollup) != "" {
		t.Errorf("SeriesComponent = %q, want \"\" (the job roll-up)", SeriesComponent(oldDockerRollup))
	}
}

func summarySeries() []Series {
	return []Series{
		{Name: "memory.usage", Labels: []Label{}},
		{Name: "memory.swap.usage", Labels: []Label{}},
		{Name: "memory.limit", Labels: []Label{}},
		{Name: "cpu.utilization", Labels: labelsOf("cpu", "total")},
		{Name: "cpu.request", Labels: []Label{}},
		{Name: "cpu.limit", Labels: []Label{}},
		// Noise the summary must drop:
		{Name: "memory.rss", Labels: []Label{}},
		{Name: "memory.cache", Labels: []Label{}},
		{Name: "memory.working_set", Labels: []Label{}},
		{Name: "cpu.utilization", Labels: labelsOf("cpu", "cpu0")},
		{Name: "cpu.utilization", Labels: labelsOf("cpu", "cpu1")},
		{Name: "memory.usage", Labels: labelsOf("component", "builder")},
		{Name: "cpu.utilization", Labels: labelsOf("component", "builder")},
	}
}

func TestSummaryViewDropsComponentsPerCoreAndDuplicateMemoryAnswers(t *testing.T) {
	got := selectSeriesForView(summarySeries(), ViewSummary, "")

	for _, s := range got {
		if SeriesComponent(s.Labels) != "" {
			t.Errorf("%s: the summary must not include per-component series", s.Name)
		}
		if isPerCoreSeries(s.Labels) {
			t.Errorf("%s: the summary must not include per-core series", s.Name)
		}
		switch s.Name {
		case "memory.rss", "memory.cache", "memory.working_set":
			t.Errorf("%s: memory.usage is the answer to \"how much memory\"; this is a duplicate", s.Name)
		}
	}
	if len(got) != 6 {
		t.Errorf("summary series count = %d, want 6 (mem usage/swap/limit, cpu util/request/limit); got %v",
			len(got), seriesNames(got))
	}
}

func TestRawViewKeepsEverything(t *testing.T) {
	all := summarySeries()
	got := selectSeriesForView(all, ViewRaw, "")
	if len(got) != len(all) {
		t.Errorf("raw view returned %d series, want all %d", len(got), len(all))
	}
}

// TestComponentFilterWorksInBothViews covers the picker: choosing a component
// must show that component's numbers whichever view is active.
func TestComponentFilterWorksInBothViews(t *testing.T) {
	for _, view := range []MetricView{ViewSummary, ViewRaw} {
		got := selectSeriesForView(summarySeries(), view, "builder")
		if len(got) != 2 {
			t.Errorf("view %q, component=builder: got %d series (%v), want 2", view, len(got), seriesNames(got))
		}
		for _, s := range got {
			if SeriesComponent(s.Labels) != "builder" {
				t.Errorf("view %q: got a %q series while filtering to builder", view, SeriesComponent(s.Labels))
			}
		}
	}
}

// TestAvailableComponentsIgnoresTheRollUp drives the UI rule "show the picker
// only when there is more than one thing to pick".
func TestAvailableComponentsIgnoresTheRollUp(t *testing.T) {
	got := AvailableComponents(summarySeries())
	if !reflect.DeepEqual(got, []string{"builder"}) {
		t.Errorf("AvailableComponents = %v, want [builder] (the job roll-up is not a component)", got)
	}

	// A single-container job reports no components at all, so the UI shows no
	// picker rather than a dropdown with one entry.
	single := []Series{{Name: "memory.usage", Labels: []Label{}}}
	if got := AvailableComponents(single); len(got) != 0 {
		t.Errorf("AvailableComponents(single-container job) = %v, want empty", got)
	}
}

func TestParseMetricViewNeverReturnsEmpty(t *testing.T) {
	for _, in := range []string{"", "summary", "raw", "nonsense"} {
		if got := ParseMetricView(in); got == "" {
			t.Errorf("ParseMetricView(%q) returned the empty view; the API default must be a real view", in)
		}
	}
	if ParseMetricView("raw") != ViewRaw {
		t.Error("ParseMetricView(\"raw\") must select the raw view")
	}
	if ParseMetricView("nonsense") != ViewSummary {
		t.Error("an unknown view must fall back to the summary, not fail")
	}
}

func seriesNames(series []Series) []string {
	out := make([]string, 0, len(series))
	for _, s := range series {
		name := s.Name
		if c := SeriesComponent(s.Labels); c != "" {
			name += "{component=" + c + "}"
		}
		out = append(out, name)
	}
	return out
}
