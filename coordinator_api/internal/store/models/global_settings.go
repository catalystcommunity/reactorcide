package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Well-known global_settings keys.
const (
	GlobalSettingNewProjectsPrivate = "new_projects_private"
)

// JSONValue is a raw JSON value stored in a jsonb column. Unlike JSONB (which
// is restricted to objects), global_settings values may be any JSON scalar,
// array, or object (for example a bare boolean for new_projects_private).
type JSONValue json.RawMessage

// Value implements driver.Valuer interface for database storage.
func (v JSONValue) Value() (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return []byte(v), nil
}

// Scan implements sql.Scanner interface for database retrieval.
func (v *JSONValue) Scan(value interface{}) error {
	if value == nil {
		*v = nil
		return nil
	}

	switch b := value.(type) {
	case []byte:
		*v = append(JSONValue(nil), JSONValue(b)...)
		return nil
	case string:
		*v = JSONValue(b)
		return nil
	default:
		return fmt.Errorf("cannot scan %T into JSONValue", value)
	}
}

// MarshalJSON implements json.Marshaler.
func (v JSONValue) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return v, nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (v *JSONValue) UnmarshalJSON(data []byte) error {
	if v == nil {
		return fmt.Errorf("JSONValue: UnmarshalJSON on nil pointer")
	}
	*v = append((*v)[0:0], data...)
	return nil
}

// GlobalSetting is a single key/value row in the global settings table.
type GlobalSetting struct {
	Key       string    `gorm:"primaryKey;type:text" json:"key"`
	Value     JSONValue `gorm:"type:jsonb;not null" json:"value"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

// TableName specifies the table name for the model.
func (GlobalSetting) TableName() string {
	return "global_settings"
}
