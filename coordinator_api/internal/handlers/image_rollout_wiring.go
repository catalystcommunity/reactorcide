package handlers

import (
	"context"
	"sync"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/lease"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
)

var imageRolloutOnce sync.Once

// startImageRolloutOnce runs the image alias promotion loop on the replica
// that holds the "image-alias-rollout" lease (docs/image-aliases.md). The
// loop moves a pending pin to the active pin when every online worker that
// serves the alias has the new image.
func startImageRolloutOnce(appStore store.Store) {
	promoterStore, ok := appStore.(imageset.PromoterStore)
	if !ok {
		return
	}
	leases, ok := appStore.(store.LeaseStore)
	if !ok {
		return
	}
	imageRolloutOnce.Do(func() {
		promoter := &imageset.Promoter{Store: promoterStore}
		go lease.NewManager(leases).Run(context.Background(), imageset.LeaseName, promoter.Run)
	})
}
