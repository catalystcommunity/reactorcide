package postgres_store

import (
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSaveWithOptionalUserIDOmitEmptyUUID(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=reactorcide dbname=reactorcide sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}

	tests := []struct {
		name   string
		value  interface{}
		userID string
	}{
		{
			name:  "userless job",
			value: &models.Job{JobID: "00000000-0000-0000-0000-000000000001", OrgID: "00000000-0000-0000-0000-000000000002"},
		},
		{
			name:  "userless workflow",
			value: &models.WorkflowInstance{WorkflowID: "00000000-0000-0000-0000-000000000003", OrgID: "00000000-0000-0000-0000-000000000002"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := saveWithOptionalUserID(db.Session(&gorm.Session{}), tt.value, tt.userID)
			if result.Error != nil {
				t.Fatalf("save dry-run model: %v", result.Error)
			}
			if strings.Contains(strings.ToLower(result.Statement.SQL.String()), "user_id") {
				t.Fatalf("save SQL contains user_id for a userless model: %s", result.Statement.SQL.String())
			}
		})
	}
}

func TestSaveWithOptionalUserIDKeepsPresentUUID(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=reactorcide dbname=reactorcide sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("open dry-run database: %v", err)
	}

	userID := "00000000-0000-0000-0000-000000000004"
	job := &models.Job{
		JobID:  "00000000-0000-0000-0000-000000000001",
		OrgID:  "00000000-0000-0000-0000-000000000002",
		UserID: userID,
	}
	result := saveWithOptionalUserID(db, job, job.UserID)
	if result.Error != nil {
		t.Fatalf("save dry-run model: %v", result.Error)
	}
	if !strings.Contains(strings.ToLower(result.Statement.SQL.String()), "user_id") {
		t.Fatalf("save SQL omits a present user_id: %s", result.Statement.SQL.String())
	}
}
