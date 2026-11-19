package requiredset

import (
	"context"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
)

// RepublishLeaseName is the lease of the republish pass. One replica runs it.
const RepublishLeaseName = "required-set-republish"

// Defaults of the republish pass.
const (
	DefaultRepublishInterval = 15 * time.Second
	// DefaultRepublishOverlap: each pass reads changes again from this long
	// before the previous pass. An audit row gets the time of its
	// transaction start, so a change that commits late is still found.
	DefaultRepublishOverlap = 2 * time.Minute
	// DefaultRepublishStartLookback: the first pass of a new lease holder
	// reads this far back, for changes that the previous holder did not
	// process.
	DefaultRepublishStartLookback = 10 * time.Minute
	// DefaultOpenEventWindow: only pull request events received in this
	// window are open for the pass.
	DefaultOpenEventWindow = 30 * 24 * time.Hour
	// maxEventsPerProject limits the provider calls for one project.
	maxEventsPerProject = 200
)

// RepublishPass recomputes the "Reactorcide Checks" status of the open pull
// request events of a project after its required set changed: a promotion,
// a demotion, a quarantine, the end of a quarantine, the expiry of a
// quarantine, or a change of the enabled setting. The classification is
// computed at read time, so only the published status can be stale.
//
// The pass finds changes in the audit events (written in the same
// transaction as the change) and in the quarantine expiry times. So it works
// the same for REST, CLI, and UI changes, and after a restart.
type RepublishPass struct {
	// Store must implement store.RequiredSetRepublishStore.
	Store any
	// Publish recomputes and posts the status of one event. It posts only
	// when the result changed (vcs.JobStatusUpdater.PublishEventChecks).
	Publish  func(ctx context.Context, eventID string) error
	Interval time.Duration
	Now      func() time.Time

	last time.Time
}

func (p *RepublishPass) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

// RunOnce processes the changes after the previous pass. It returns the
// number of events it published.
func (p *RepublishPass) RunOnce(ctx context.Context) (int, error) {
	st, ok := p.Store.(store.RequiredSetRepublishStore)
	if !ok || p.Publish == nil {
		return 0, nil
	}
	now := p.now()
	since := now.Add(-DefaultRepublishStartLookback)
	if !p.last.IsZero() {
		since = p.last.Add(-DefaultRepublishOverlap)
	}
	projects, err := st.ListRequiredSetChangedProjects(ctx, since, now)
	if err != nil {
		return 0, err
	}
	published, failed := 0, false
	for _, projectID := range projects {
		if ctx.Err() != nil {
			return published, ctx.Err()
		}
		events, err := st.ListOpenPullRequestEventIDs(ctx, projectID, now.Add(-DefaultOpenEventWindow), maxEventsPerProject)
		if err != nil {
			logging.Log.WithError(err).WithField("project_id", projectID).Warn("Failed to list open events after a required set change")
			failed = true
			continue
		}
		for _, eventID := range events {
			if err := p.Publish(ctx, eventID); err != nil {
				logging.Log.WithError(err).WithField("event_id", eventID).Warn("Failed to republish event checks after a required set change")
				continue
			}
			published++
		}
	}
	// A failed listing keeps p.last, so the next pass reads the change again.
	// A failed publish does not: the next event change publishes again.
	if !failed {
		p.last = now
	}
	return published, nil
}

// Run runs a pass on each interval until ctx is done.
func (p *RepublishPass) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultRepublishInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := p.RunOnce(ctx); err != nil && ctx.Err() == nil {
			logging.Log.WithError(err).Warn("Required set republish pass failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
