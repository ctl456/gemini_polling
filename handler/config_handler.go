package handler

import (
	"gemini_polling/config"
	"net/http"

	"github.com/gin-gonic/gin"
)

type ConfigHandler struct {
	configManager *config.Manager
}

func NewConfigHandler(manager *config.Manager) *ConfigHandler {
	return &ConfigHandler{configManager: manager}
}

// GetSettings 从管理器获取当前配置
func (h *ConfigHandler) GetSettings(c *gin.Context) {
	currentConfig := h.configManager.Get()
	safeSettings := map[string]interface{}{
		"SERVER_PORT":         currentConfig.Port,
		"ADMIN_API_KEY":       currentConfig.AdminAPIKey,
		"POLLING_API_KEY":     currentConfig.PollingAPIKey,
		"DB_DRIVER":           currentConfig.DBDriver,
		"SQLITE_PATH":         currentConfig.SQLitePath,
		"MYSQL_USER":          currentConfig.MySQLUser,
		"MYSQL_PASSWORD":      "", // 始终不返回密码
		"MYSQL_HOST":          currentConfig.MySQLHost,
		"MYSQL_PORT":          currentConfig.MySQLPort,
		"MYSQL_DBNAME":        currentConfig.MySQLDBName,
		"MAX_RETRIES":         currentConfig.MaxRetries,
		"RATE_LIMIT_COOLDOWN": int(currentConfig.RateLimitCooldown.Seconds()),
		"HEALTH_CHECK_CONCURRENCY": currentConfig.HealthCheckConcurrency,
		"LOG_LEVEL":          currentConfig.LogLevel,
		"LOG_TO_FILE":        currentConfig.LogToFile,
		"LOG_FILE":           currentConfig.LogFile,
		"MAX_LOG_SIZE_MB":    currentConfig.MaxLogSizeMB,
		"MAX_LOG_BACKUPS":    currentConfig.MaxLogBackups,
		"MAX_LOG_AGE_DAYS":   currentConfig.MaxLogAgeDays,
		"MIN_HEALTH_SCORE":   currentConfig.MinHealthScore,
		"MAX_429_COUNT":      currentConfig.Max429Count,
		"RECOVERY_BONUS":     currentConfig.RecoveryBonus,
		"PENALTY_FACTOR":     currentConfig.PenaltyFactor,
	}
	c.JSON(http.StatusOK, safeSettings)
}

// UpdateSettings 更新 .env 文件并触发热重载
func (h *ConfigHandler) UpdateSettings(c *gin.Context) {
	var updates map[string]string
	if err := c.ShouldBindJSON(&updates); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求格式: " + err.Error()})
		return
	}

	if err := config.UpdateEnvFile(updates); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新 .env 文件失败: " + err.Error()})
		return
	}

	// 触发配置热重载
	if err := h.configManager.ReloadAndUpdate(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "配置热重载失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "配置已成功更新并立即生效！(注意: 数据库和端口相关设置需要重启)",
	})
}
