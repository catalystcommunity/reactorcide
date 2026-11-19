package postgres_store

import (
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// saveWithOptionalUserID saves a model that uses an empty string to represent
// a NULL user_id. GORM applies the column default during INSERT, but Save writes
// every field during UPDATE. Omit the empty UUID so PostgreSQL keeps the
// existing NULL value.
func saveWithOptionalUserID(db *gorm.DB, value interface{}, userID string) *gorm.DB {
	// GORM's Omit replaces the earlier list, so build one list.
	omit := []string{}
	if userID == "" {
		omit = append(omit, "user_id")
	}
	// The actor is set once, at insert (a database trigger fills it when the
	// creator does not). A struct that never read it back must not erase it.
	if actorUnknown(value) {
		omit = append(omit, "actor_principal_id", "actor_identity")
	}
	if len(omit) > 0 {
		db = db.Omit(omit...)
	}
	return db.Save(value)
}

func actorUnknown(value interface{}) bool {
	switch row := value.(type) {
	case *models.Job:
		return row.ActorPrincipalID == nil
	case *models.WorkflowInstance:
		return row.ActorPrincipalID == nil
	}
	return false
}

// refreshActor reads the actor that the database trigger gave a new row.
func refreshActor(db *gorm.DB, table, keyColumn, key string, principalID **string, identity *string) {
	if *principalID != nil || key == "" {
		return
	}
	var row struct {
		ActorPrincipalID *string
		ActorIdentity    string
	}
	if db.Table(table).Select("actor_principal_id, actor_identity").Where(keyColumn+" = ?", key).Take(&row).Error == nil {
		*principalID, *identity = row.ActorPrincipalID, row.ActorIdentity
	}
}
