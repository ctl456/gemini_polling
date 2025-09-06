package model

import (
	"time"
)

// APIKey 是数据库中 api_keys 表的 GORM 模型
// model/api_key.go
type APIKey struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Key       string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"key"`
	Enabled   bool      `gorm:"index;not null;default:true" json:"enabled"` // <- 添加 index 标签
	CreatedAt time.Time `json:"created_at"`
	
	// 新增字段用于智能key管理
	HealthScore      int       `gorm:"default:100" json:"health_score"`        // 健康分数 0-100
	LastUsedAt       time.Time `json:"last_used_at"`                           // 最后使用时间
	Last429At        time.Time `json:"last_429_at"`                            // 最后429时间
	SuccessCount     int       `gorm:"default:0" json:"success_count"`         // 成功次数
	FailureCount     int       `gorm:"default:0" json:"failure_count"`         // 失败次数
	RateLimitCount   int       `gorm:"default:0" json:"rate_limit_count"`      // 429次数
	NextAvailableAt  time.Time `json:"next_available_at"`                     // 下次可用时间
	IsOnCooldown     bool      `gorm:"default:false" json:"is_on_cooldown"`    // 是否在冷却中
}
