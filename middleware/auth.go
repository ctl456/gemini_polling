package middleware

import (
	"gemini_polling/config" // 引入 config
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AdminAuthMiddleware 现在接收 ConfigManager
func AdminAuthMiddleware(manager *config.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 每次请求都获取最新的 Admin Key
		adminAPIKey := manager.Get().AdminAPIKey
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is required"})
			return
		}
		// ... 验证逻辑不变...
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header format must be Bearer {token}"})
			return
		}

		if parts[1] != adminAPIKey {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Invalid admin API key"})
			return
		}

		c.Next()
	}
}

// PollingAuthMiddleware 现在接收 ConfigManager
func PollingAuthMiddleware(manager *config.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 每次请求都获取最新的 Polling Key
		requiredKey := manager.Get().PollingAPIKey
		if requiredKey == "" {
			c.Next()
			return
		}
		// ... 验证逻辑不变...
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is required"})
			c.Abort()
			return
		}
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header format must be Bearer {token}"})
			c.Abort()
			return
		}
		if parts[1] != requiredKey {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API Key"})
			c.Abort()
			return
		}

		c.Next()
	}
}
