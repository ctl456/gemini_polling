package model

import "time"

// APIKey 是数据库中 api_keys 表的 GORM 模型
// model/api_key.go
type APIKey struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Key       string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"key"`
	Enabled   bool      `gorm:"index;not null;default:true" json:"enabled"` // <- 添加 index 标签
	CreatedAt time.Time `json:"created_at"`
}
