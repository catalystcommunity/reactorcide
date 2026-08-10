package models

import "time"

type WorkerClass struct {
	ClassID   string    `gorm:"column:class_id;primaryKey;type:uuid;default:generate_ulid()" json:"class_id"`
	OrgID     string    `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	Name      string    `gorm:"type:text;not null" json:"name"`
	Protected bool      `gorm:"not null;default:false" json:"protected"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (WorkerClass) TableName() string { return "worker_classes" }

type WorkerClassPool struct {
	ClassID   string    `gorm:"column:class_id;primaryKey;type:uuid" json:"class_id"`
	PoolID    string    `gorm:"column:pool_id;primaryKey;type:uuid" json:"pool_id"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

func (WorkerClassPool) TableName() string { return "worker_class_pools" }
