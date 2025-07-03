package storage

import (
	"fmt"
	"gemini_polling/config"
	"gemini_polling/model" // <-- 注意导入路径的变化
	"log"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func InitDB(cfg *config.Config) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch cfg.DBDriver {
	case "mysql":
		log.Println("正在通过 GORM 初始化 MySQL 数据库...")
		dialector = mysql.Open(cfg.MySQLDSN)
	case "sqlite3":
		log.Println("正在通过 GORM 初始化 SQLite3 数据库...")
		dialector = sqlite.Open(cfg.SQLitePath)
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s", cfg.DBDriver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("GORM 打开数据库连接失败: %w", err)
	}

	log.Println("正在进行数据库迁移 (AutoMigrate)...")
	// 使用新的模型位置
	if err := db.AutoMigrate(&model.APIKey{}); err != nil {
		return nil, fmt.Errorf("GORM 自动迁移失败: %w", err)
	}
	log.Println("api_keys 表已成功初始化/迁移。")

	return db, nil
}
