// storage/database.go

package storage

import (
	"database/sql"
	"fmt"
	"gemini_polling/config"
	"gemini_polling/model"
	"log"
	"os"
	"path/filepath"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

func InitDB(cfg *config.Config) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch cfg.DBDriver {
	case "mysql":
		log.Println("正在通过 GORM 初始化 MySQL 数据库...")
		dialector = mysql.Open(cfg.MySQLDSN)
	case "sqlite3":
		log.Println("正在通过 GORM 初始化 SQLite3 数据库...")
		// 在连接前，确保数据库文件所在的目录存在
		dbPath := cfg.SQLitePath
		dbDir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			return nil, fmt.Errorf("创建数据库目录 %s 失败: %w", dbDir, err)
		}
		log.Printf("确保数据库目录 '%s' 已存在。", dbDir)
		dialector = sqlite.Open(dbPath)
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s", cfg.DBDriver)
	}

	// 配置 GORM 选项
	gormConfig := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent), // 关闭 SQL 日志以提升性能
	}

	db, err := gorm.Open(dialector, gormConfig)
	if err != nil {
		return nil, fmt.Errorf("GORM 打开数据库连接失败: %w", err)
	}

	// 获取底层 sql.DB 对象以配置连接池
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取 sql.DB 失败: %w", err)
	}

	// 根据数据库类型优化连接池配置
	switch cfg.DBDriver {
	case "mysql":
		// MySQL 连接池优化
		sqlDB.SetMaxIdleConns(25)                    // 最大空闲连接数
		sqlDB.SetMaxOpenConns(100)                   // 最大打开连接数
		sqlDB.SetConnMaxLifetime(5 * time.Minute)     // 连接最大存活时间
		sqlDB.SetConnMaxIdleTime(2 * time.Minute)    // 空闲连接最大存活时间
		
	case "sqlite3":
		// SQLite 连接池优化 (考虑写入锁限制)
		sqlDB.SetMaxOpenConns(1)    // SQLite 写入锁限制，避免并发写入问题
		sqlDB.SetMaxIdleConns(1)    // 空闲连接数
		
		// SQLite 性能优化 PRAGMA 设置
		if err := optimizeSQLite(sqlDB); err != nil {
			log.Printf("SQLite 性能优化警告: %v", err)
		}
	}

	log.Println("正在进行数据库迁移 (AutoMigrate)...")
	if err := db.AutoMigrate(&model.APIKey{}); err != nil {
		return nil, fmt.Errorf("GORM 自动迁移失败: %w", err)
	}
	log.Println("api_keys 表已成功初始化/迁移。")
	
	// 检查是否需要添加新字段的默认值
	if err := updateExistingKeys(db); err != nil {
		log.Printf("更新现有记录默认值时出现警告: %v", err)
	}
	
	return db, nil
}

// updateExistingKeys 为现有记录设置默认值
func updateExistingKeys(db *gorm.DB) error {
	// 检查是否有 HealthScore 字段为空的记录
	var count int64
	db.Model(&model.APIKey{}).Where("health_score IS NULL").Count(&count)
	
	if count > 0 {
		log.Printf("发现 %d 条记录需要更新默认值，正在处理...", count)
		
		// 更新健康分数相关字段
		result := db.Model(&model.APIKey{}).Where("health_score IS NULL").Updates(map[string]interface{}{
			"health_score":       100,
			"success_count":      0,
			"failure_count":      0,
			"rate_limit_count":   0,
			"is_on_cooldown":     false,
			"last_used_at":       time.Now(),
			"last_429_at":        time.Time{},
			"next_available_at":  time.Time{},
		})
		
		if result.Error != nil {
			return fmt.Errorf("更新默认值失败: %w", result.Error)
		}
		
		log.Printf("成功更新 %d 条记录的默认值", result.RowsAffected)
	}
	
	return nil
}

// optimizeSQLite 执行 SQLite 性能优化
func optimizeSQLite(sqlDB *sql.DB) error {
	// SQLite WAL 模式和性能优化
	pragmaSettings := map[string]string{
		"journal_mode":      "WAL",         // 使用 WAL 模式提升并发性能
		"synchronous":       "NORMAL",      // 平衡性能和数据安全
		"cache_size":        "-10000",      // 10MB 缓存
		"temp_store":        "MEMORY",      // 临时数据存储在内存
		"mmap_size":         "268435456",   // 256MB 内存映射
		"busy_timeout":      "5000",        // 5秒忙碌超时
		"foreign_keys":      "ON",          // 启用外键约束
	}

	for pragma, value := range pragmaSettings {
		if _, err := sqlDB.Exec(fmt.Sprintf("PRAGMA %s = %s", pragma, value)); err != nil {
			log.Printf("设置 PRAGMA %s = %s 失败: %v", pragma, value, err)
			// 不返回错误，因为一些 PRAGMA 可能不被支持
		}
	}
	
	return nil
}
