// storage/database.go

package storage

import (
	"fmt"
	"gemini_polling/config"
	"gemini_polling/model"
	"log"
	"os"            // <-- 新增
	"path/filepath" // <-- 新增

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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
		// --- 开始修改 ---
		// 在连接前，确保数据库文件所在的目录存在
		dbPath := cfg.SQLitePath
		dbDir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dbDir, 0755); err != nil {
			return nil, fmt.Errorf("创建数据库目录 %s 失败: %w", dbDir, err)
		}
		log.Printf("确保数据库目录 '%s' 已存在。", dbDir)
		// --- 结束修改 ---
		dialector = sqlite.Open(dbPath) // 使用 dbPath 变量
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s", cfg.DBDriver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("GORM 打开数据库连接失败: %w", err)
	}

	log.Println("正在进行数据库迁移 (AutoMigrate)...")
	if err := db.AutoMigrate(&model.APIKey{}); err != nil {
		return nil, fmt.Errorf("GORM 自动迁移失败: %w", err)
	}
	log.Println("api_keys 表已成功初始化/迁移。")

	return db, nil
}
