package githubapp

import (
	"sort"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// AppHeadroom is the rate-limit headroom of one App, from 0 (no budget left)
// to 1 (full budget).
type AppHeadroom struct {
	AppID int64 `json:"app_id"`
	// Headroom is the lowest headroom of the App snapshot and of the
	// snapshots of its active installations. A snapshot past its reset time
	// counts as full.
	Headroom float64 `json:"headroom"`
	// ActiveInstallations is the number of active installations.
	ActiveInstallations int `json:"active_installations"`
	// Repositories is the number of bound repositories.
	Repositories int `json:"repositories"`
}

// ComputeHeadroom returns the headroom of each App.
func ComputeHeadroom(apps []models.GitHubApp, installations []models.GitHubAppInstallation, now time.Time) map[int64]AppHeadroom {
	out := make(map[int64]AppHeadroom, len(apps))
	for _, app := range apps {
		out[app.AppID] = AppHeadroom{AppID: app.AppID, Headroom: app.RateLimit.Headroom(now)}
	}
	for _, installation := range installations {
		entry, ok := out[installation.AppID]
		if !ok || !installation.Active() {
			continue
		}
		entry.ActiveInstallations++
		entry.Repositories += installation.RepositoryCount
		if value := installation.RateLimit.Headroom(now); value < entry.Headroom {
			entry.Headroom = value
		}
		out[installation.AppID] = entry
	}
	return out
}

// SelectApp returns the enabled App that a new installation (a new
// repository registration) should use: the most rate-limit headroom, then
// the fewest bound repositories, then the lowest App ID. ok is false when no
// App is enabled.
func SelectApp(apps []models.GitHubApp, installations []models.GitHubAppInstallation, now time.Time) (models.GitHubApp, AppHeadroom, bool) {
	headroom := ComputeHeadroom(apps, installations, now)
	candidates := make([]models.GitHubApp, 0, len(apps))
	for _, app := range apps {
		if app.Enabled {
			candidates = append(candidates, app)
		}
	}
	if len(candidates) == 0 {
		return models.GitHubApp{}, AppHeadroom{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := headroom[candidates[i].AppID], headroom[candidates[j].AppID]
		if a.Headroom != b.Headroom {
			return a.Headroom > b.Headroom
		}
		if a.Repositories != b.Repositories {
			return a.Repositories < b.Repositories
		}
		return candidates[i].AppID < candidates[j].AppID
	})
	return candidates[0], headroom[candidates[0].AppID], true
}

// InstallURL is the page where an account installs the App.
func InstallURL(app models.GitHubApp) string {
	if app.Slug == "" {
		return ""
	}
	return "https://github.com/apps/" + app.Slug + "/installations/new"
}
