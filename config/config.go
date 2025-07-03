package config

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// Config 结构体保持不变
type Config struct {
	Port              string
	AdminAPIKey       string
	DBDriver          string
	MySQLDSN          string
	SQLitePath        string
	PollingAPIKey     string
	MaxRetries        int
	RateLimitCooldown time.Duration
	MySQLUser         string
	MySQLPassword     string
	MySQLHost         string
	MySQLPort         string
	MySQLDBName       string
}

// Manager 结构体用于管理全局配置，并支持热重载
type Manager struct {
	mu     sync.RWMutex
	config *Config
}

// 全局配置管理器实例
var GlobalConfigManager *Manager

// InitConfigManager 初始化全局配置管理器
func InitConfigManager() (*Manager, error) {
	cfg, err := loadFromFile()
	if err != nil {
		return nil, fmt.Errorf("初始化配置失败: %w", err)
	}
	GlobalConfigManager = &Manager{
		config: cfg,
	}
	log.Println("配置管理器初始化成功。")
	return GlobalConfigManager, nil
}

// Get 返回当前配置的只读副本，保证线程安全
func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// 返回一个副本以防止外部修改
	cfgCopy := *m.config
	return &cfgCopy
}

// ReloadAndUpdate 会重新从 .env 文件加载配置，并原子性地更新内部配置
func (m *Manager) ReloadAndUpdate() error {
	newCfg, err := loadFromFile()
	if err != nil {
		return fmt.Errorf("重载配置失败: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = newCfg

	log.Println("配置已成功热重载！")
	return nil
}

// loadFromFile 是实际从 .env 加载配置的私有函数
func loadFromFile() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		fmt.Println("警告: .env 文件未找到, 将依赖系统环境变量。")
	}

	maxRetries, err := strconv.Atoi(getEnv("MAX_RETRIES", "5"))
	if err != nil {
		fmt.Printf("警告: MAX_RETRIES 值无效, 使用默认值 5。错误: %v\n", err)
		maxRetries = 5
	}

	cooldownSeconds, err := strconv.Atoi(getEnv("RATE_LIMIT_COOLDOWN", "60"))
	if err != nil {
		fmt.Printf("警告: RATE_LIMIT_COOLDOWN 值无效, 使用默认值 60。错误: %v\n", err)
		cooldownSeconds = 60
	}

	cfg := &Config{
		Port:              getEnv("SERVER_PORT", "8080"),
		AdminAPIKey:       getEnv("ADMIN_API_KEY", "fallback-admin-key"),
		DBDriver:          getEnv("DB_DRIVER", "sqlite3"),
		SQLitePath:        getEnv("SQLITE_PATH", "./data.db"),
		PollingAPIKey:     getEnv("POLLING_API_KEY", ""),
		MaxRetries:        maxRetries,
		RateLimitCooldown: time.Duration(cooldownSeconds) * time.Second,
		MySQLUser:         getEnv("MYSQL_USER", "root"),
		MySQLPassword:     getEnv("MYSQL_PASSWORD", ""),
		MySQLHost:         getEnv("MYSQL_HOST", "127.0.0.1"),
		MySQLPort:         getEnv("MYSQL_PORT", "3306"),
		MySQLDBName:       getEnv("MYSQL_DBNAME", "test_db"),
	}

	if cfg.DBDriver == "mysql" {
		cfg.MySQLDSN = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
			cfg.MySQLUser, cfg.MySQLPassword, cfg.MySQLHost, cfg.MySQLPort, cfg.MySQLDBName,
		)
	}

	return cfg, nil
}

// getEnv 和 UpdateEnvFile 保持不变
func getEnv(key, fallback string) string {
	// ...
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
func UpdateEnvFile(updates map[string]string) error {
	// ...
	envFilePath := ".env"

	// 确保文件存在，如果不存在则创建一个空的
	file, err := os.OpenFile(envFilePath, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return fmt.Errorf("打开 .env 文件失败: %w", err)
	}
	defer file.Close()

	// 读取现有内容
	scanner := bufio.NewScanner(file)
	var lines []string
	existingKeys := make(map[string]bool)

	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			lines = append(lines, line)
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			if newValue, ok := updates[key]; ok {
				// 更新现有行
				lines = append(lines, fmt.Sprintf("%s=%s", key, newValue))
				existingKeys[key] = true
			} else {
				// 保留未更改的行
				lines = append(lines, line)
			}
		} else {
			lines = append(lines, line) //保留格式不正确的行
		}
	}

	// 补全 .env 文件中没有的、但前端传过来的密码字段
	for key, val := range updates {
		if !existingKeys[key] {
			lines = append(lines, fmt.Sprintf("%s=%s", key, val))
			existingKeys[key] = true
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 .env 文件失败: %w", err)
	}

	// 将更新后的内容写回文件
	err = file.Truncate(0)
	if err != nil {
		return fmt.Errorf("清空 .env 文件失败: %w", err)
	}
	_, err = file.Seek(0, 0)
	if err != nil {
		return fmt.Errorf("重置文件指针失败: %w", err)
	}

	writer := bufio.NewWriter(file)
	for _, line := range lines {
		_, err := fmt.Fprintln(writer, line)
		if err != nil {
			return fmt.Errorf("写入 .env 文件失败: %w", err)
		}
	}

	return writer.Flush()
}
