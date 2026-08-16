package postgres_store

import "gorm.io/gorm"

// saveWithOptionalUserID saves a model that uses an empty string to represent
// a NULL user_id. GORM applies the column default during INSERT, but Save writes
// every field during UPDATE. Omit the empty UUID so PostgreSQL keeps the
// existing NULL value.
func saveWithOptionalUserID(db *gorm.DB, value interface{}, userID string) *gorm.DB {
	if userID == "" {
		db = db.Omit("user_id")
	}
	return db.Save(value)
}
