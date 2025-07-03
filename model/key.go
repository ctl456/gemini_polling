package model

import "gorm.io/gorm"

// BackendAPIKey 存储在数据库中的Google GenAI Key
type BackendAPIKey struct {
	gorm.Model
	Key     string `gorm:"unique;not null"`
	Enabled bool   `gorm:"default:true"`
}
