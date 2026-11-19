package vcs

import (
	"context"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/sirupsen/logrus"
)

// Report modes (plan item A3, docs/reporting-and-ownership.md). Every VCS
// write of the status updater asks the mode of the project first:
//
//   - full: statuses and the pull request comment (the default).
//   - statuses_only: statuses; no pull request comment.
//   - silent: no VCS write. The results are only in the UI and the API.

func (u *JobStatusUpdater) reportMode(ctx context.Context, orgID string, projectID *string) string {
	if u == nil || u.store == nil {
		return models.ReportModeFull
	}
	id := ""
	if projectID != nil {
		id = *projectID
	}
	return reporting.Mode(ctx, u.store, orgID, id)
}

// statusesAllowed reports whether the project gets commit statuses.
func (u *JobStatusUpdater) statusesAllowed(ctx context.Context, orgID string, projectID *string) bool {
	return reporting.AllowsStatuses(u.reportMode(ctx, orgID, projectID))
}

// commentsAllowed reports whether the project gets the pull request comment.
func (u *JobStatusUpdater) commentsAllowed(ctx context.Context, orgID string, projectID *string) bool {
	return reporting.AllowsComments(u.reportMode(ctx, orgID, projectID))
}

// ProjectClient returns the VCS client of a project (the project credential
// first, then the global client of the provider). Other packages use it to
// read from the VCS, for example the code owners file at admission.
func (u *JobStatusUpdater) ProjectClient(ctx context.Context, projectID string, provider Provider) Client {
	if u == nil {
		return nil
	}
	id := projectID
	if client := u.getProjectClient(ctx, &id, provider); client != nil {
		return client
	}
	return u.vcsClients[provider]
}

// CommitResultsReader reads the results of the other CI systems on a commit:
// the commit statuses and the check runs.
type CommitResultsReader interface {
	ListCommitCIResults(ctx context.Context, repo, sha string) ([]models.AgreementOtherResult, error)
}

// recordAgreementResult stores the Reactorcide result of the commit of an
// event for the agreement report, when the project has agreement contexts
// and the event has a final result. It never fails the status publish.
func (u *JobStatusUpdater) recordAgreementResult(ctx context.Context, event *models.CIEvent, state, description string) {
	if event == nil || event.ProjectID == nil || *event.ProjectID == "" || event.CommitSHA == "" {
		return
	}
	reports, ok := u.store.(store.ReportingStore)
	if !ok {
		return
	}
	result, final := reporting.ReactorcideResult(state)
	if !final {
		return
	}
	settings, err := reporting.Effective(ctx, u.store, event.OrgID, *event.ProjectID)
	if err != nil || len(settings.AgreementContexts) == 0 {
		return
	}
	eventID := event.EventID
	row := &models.CIAgreement{ProjectID: *event.ProjectID, OrgID: event.OrgID, CommitSHA: event.CommitSHA, EventID: &eventID,
		Provider: event.Provider, Repository: event.Repo, PRNumber: event.PRNumber, Branch: event.Branch,
		ReactorcideResult: result, ReactorcideDescription: description, ReactorcideCompletedAt: time.Now().UTC()}
	if err := reports.RecordCIAgreementResult(ctx, row); err != nil {
		u.logger.WithError(err).WithField("event_id", event.EventID).Warn("Could not record the agreement result")
	}
}

// AgreementPass reads the other CI results of due agreement rows. The
// coordinator runs it on the one replica that holds the agreement lease.
type AgreementPass struct {
	Updater  *JobStatusUpdater
	Interval time.Duration
	Now      func() time.Time
	// Clients overrides the client lookup (tests).
	Clients func(ctx context.Context, row models.CIAgreement) CommitResultsReader
}

func (p *AgreementPass) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *AgreementPass) reader(ctx context.Context, row models.CIAgreement) CommitResultsReader {
	if p.Clients != nil {
		return p.Clients(ctx, row)
	}
	client := p.Updater.ProjectClient(ctx, row.ProjectID, Provider(row.Provider))
	reader, _ := client.(CommitResultsReader)
	return reader
}

// RunOnce processes the due rows. It returns the number of rows it read.
func (p *AgreementPass) RunOnce(ctx context.Context) (int, error) {
	if p.Updater == nil {
		return 0, nil
	}
	reports, ok := p.Updater.store.(store.ReportingStore)
	if !ok {
		return 0, nil
	}
	rows, err := reports.ListDueCIAgreements(ctx, p.now(), 50)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		settings, err := reporting.Effective(ctx, p.Updater.store, row.OrgID, row.ProjectID)
		if err != nil {
			continue
		}
		now := p.now()
		var next models.CIAgreement
		reader := p.reader(ctx, row)
		switch {
		case len(settings.AgreementContexts) == 0:
			// The contexts were removed: close the row with what it has.
			next = reporting.Close(row)
		case reader == nil:
			next = reporting.ObserveError(row, errNoResultsReader, now)
		default:
			readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			observed, readErr := reader.ListCommitCIResults(readCtx, row.Repository, row.CommitSHA)
			cancel()
			if readErr != nil {
				next = reporting.ObserveError(row, readErr, now)
			} else {
				next = reporting.Observe(row, observed, settings.AgreementContexts, now)
			}
		}
		if err := reports.UpdateCIAgreementObservation(ctx, &next); err != nil {
			p.Updater.logger.WithError(err).WithFields(logrus.Fields{"project_id": row.ProjectID, "sha": row.CommitSHA}).
				Warn("Could not store the agreement observation")
		}
	}
	return len(rows), nil
}

// Run processes due rows until ctx ends.
func (p *AgreementPass) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := p.RunOnce(ctx); err != nil && ctx.Err() == nil {
			p.Updater.logger.WithError(err).Warn("Agreement pass failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type agreementError string

func (e agreementError) Error() string { return string(e) }

const errNoResultsReader = agreementError("the VCS client of the project cannot read commit results")
