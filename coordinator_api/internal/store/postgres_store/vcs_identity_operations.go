package postgres_store

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

func (ps PostgresDbStore) CreateVCSIdentityLink(ctx context.Context, link *models.VCSIdentityLink) error {
	link.Provider = strings.ToLower(strings.TrimSpace(link.Provider))
	link.ExternalSubject = strings.ToLower(strings.TrimSpace(link.ExternalSubject))
	if link.Provider == "" || link.ExternalSubject == "" {
		return fmt.Errorf("provider and external subject are required")
	}
	return ps.getDB(ctx).Create(link).Error
}

func (ps PostgresDbStore) ResolveVCSIdentityLink(ctx context.Context, provider, subject string) (*models.VCSIdentityLink, error) {
	var link models.VCSIdentityLink
	err := ps.getDB(ctx).Where("provider = ? AND external_subject = ?", strings.ToLower(provider), strings.ToLower(subject)).First(&link).Error
	if err == gorm.ErrRecordNotFound {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &link, nil
}

func (ps PostgresDbStore) ListVCSIdentityLinks(ctx context.Context) ([]models.VCSIdentityLink, error) {
	var links []models.VCSIdentityLink
	err := ps.getDB(ctx).Order("provider, external_subject").Find(&links).Error
	return links, err
}

func (ps PostgresDbStore) DeleteVCSIdentityLink(ctx context.Context, linkID string) error {
	return ps.getDB(ctx).Where("link_id = ?", linkID).Delete(&models.VCSIdentityLink{}).Error
}
