package middleware

import (
	"gemini_polling/config"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AdminAuthMiddleware 验证管理员API密钥
func AdminAuthMiddleware(manager *config.Manager) gin.HandlerFunc {
	// (这个函数保持不变，无需修改)
	return func(c *gin.Context) {
		requiredKey := manager.Get().AdminAPIKey
		if requiredKey == "" || requiredKey == "fallback-admin-key" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin API key is not configured on the server. Access denied."})
			c.Abort()
			return
		}

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
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid Admin API Key"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// +++ 修改后的 PollingAuthMiddleware +++
// PollingAuthMiddleware 验证公共API的密钥
// 现在它同时支持 "Authorization: Bearer <key>" 和 "x-goog-api-key: <key>"
func PollingAuthMiddleware(manager *config.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		requiredKey := manager.Get().PollingAPIKey
		// 如果服务器没有配置公共密钥，则直接放行所有请求
		if requiredKey == "" {
			c.Next()
			return
		}

		var providedKey string

		// 1. 优先尝试从 "Authorization: Bearer" 中获取密钥 (OpenAI 格式)
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			parts := strings.Split(authHeader, " ")
			if len(parts) == 2 && parts[0] == "Bearer" {
				providedKey = parts[1]
			}
		}

		// 2. 如果没有从 Authorization 中获取到，则尝试从 "x-goog-api-key" 获取 (Gemini 格式)
		if providedKey == "" {
			googleKey := c.GetHeader("x-goog-api-key")
			if googleKey != "" {
				providedKey = googleKey
			}
		}

		// 3. 检查是否获取到了任何密钥
		if providedKey == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "API key is required. Provide it in 'Authorization: Bearer <key>' or 'x-goog-api-key: <key>' header."})
			c.Abort()
			return
		}

		// 4. 比较密钥是否正确
		if providedKey != requiredKey {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API Key"})
			c.Abort()
			return
		}

		// 5. 认证成功
		c.Next()
	}
}
