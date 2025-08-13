package main

import (
	"fmt"
	"gemini_polling/config"
	"gemini_polling/handler"
	"gemini_polling/middleware"
	"gemini_polling/service"
	"gemini_polling/storage"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func main() {
	// 初始化配置管理器
	configManager, err := config.InitConfigManager()
	if err != nil {
		log.Fatalf("无法初始化配置: %v", err)
	}

	// 首次获取配置
	cfg := configManager.Get()

	// ... 日志警告 ...
	if cfg.AdminAPIKey == "fallback-admin-key" || cfg.AdminAPIKey == "" {
		log.Println("警告: ADMIN_API_KEY 未设置或使用的是默认值。为了安全，请在 .env 文件或环境变量中设置一个复杂的值。")
	}
	if cfg.PollingAPIKey == "" {
		log.Println("警告: POLLING_API_KEY 未设置。/v1 路径将无需认证即可访问。")
	}

	// 注意：数据库配置是启动时确定的，通常不建议热重载数据库连接。
	// 所以数据库初始化仍然使用首次加载的配置。
	db, err := storage.InitDB(cfg)
	if err != nil {
		log.Fatalf("无法初始化数据库: %v", err)
	}
	log.Println("数据库初始化成功")

	keyStore := storage.NewKeyStore(db)

	// +++ 新增: 初始化并启动 Key 池 +++
	keyPool := service.NewKeyPool(keyStore, configManager)
	keyPool.Start(5 * time.Minute) // 每5分钟与数据库同步一次

	// GenAIService 现在也需要接收 ConfigManager 以便动态获取最新配置
	genaiService := service.NewGenAIService(configManager, keyStore, keyPool)

	// 设置为每小时扫描一次
	healthChecker := service.NewKeyHealthChecker(keyStore, genaiService, keyPool, configManager)
	healthChecker.StartPeriodicChecks(1 * time.Hour) // 你可以调整这个间隔

	// 各个 Handler 现在接收 ConfigManager
	keyHandler := handler.NewKeyHandler(keyStore, genaiService, configManager, healthChecker, keyPool)
	chatHandler := handler.NewChatHandler(genaiService)
	configHandler := handler.NewConfigHandler(configManager)

	router := gin.Default()

	// --- 路由定义区 ---
	// 根路径重定向
	router.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/admin/login.html")
	})

	// 静态文件
	router.StaticFS("/admin", http.Dir("./static"))

	// 聊天API
	v1 := router.Group("/v1")
	// 中间件现在需要动态获取配置
	v1.Use(middleware.PollingAuthMiddleware(configManager))
	{
		v1.POST("/chat/completions", chatHandler.HandleChatCompletions)
		v1.GET("/models", chatHandler.ListModels)
	}

	// gemini 格式api
	v1beta := router.Group("/v1beta")
	v1beta.Use(middleware.PollingAuthMiddleware(configManager))
	{
		v1beta.GET("/models", chatHandler.ListModels2)
		// +++ 新增的 Gemini 原生文本生成路由 +++
		v1beta.POST("/models/*model_and_action", chatHandler.HandleGeminiAction)
	}

	// 管理API
	adminApiGroup := router.Group("/api/admin")
	{
		adminApiGroup.POST("/login", keyHandler.Login)

		keysGroup := adminApiGroup.Group("/keys")
		keysGroup.Use(middleware.AdminAuthMiddleware(configManager))
		{
			// ... keys 路由不变
			keysGroup.POST("/scan", keyHandler.ScanAllKeysHandler) // 统一的扫描入口
			keysGroup.POST("", keyHandler.AddKey)
			keysGroup.GET("", keyHandler.ListKeys)
			// +++ 新增路由 +++
			keysGroup.GET("/banned", keyHandler.ListBannedKeys)
			keysGroup.DELETE("/:id", keyHandler.DeleteKey)
			keysGroup.POST("/batch-add", keyHandler.BatchAddKeys)
			keysGroup.POST("/batch-delete", keyHandler.BatchDeleteKeys)
			keysGroup.POST("/:id/check", keyHandler.CheckSingleKey)
		}

		settingsGroup := adminApiGroup.Group("/settings")
		settingsGroup.Use(middleware.AdminAuthMiddleware(configManager))
		{
			settingsGroup.GET("", configHandler.GetSettings)
			settingsGroup.POST("", configHandler.UpdateSettings)
		}
	}

	// ... (服务器启动日志不变)
	serverAddr := fmt.Sprintf(":%s", cfg.Port)
	log.Println("=========================================================")
	log.Printf("  服务器正在启动，监听地址: http://localhost%s", serverAddr)
	log.Printf("  管理后台登录地址:     http://localhost%s/admin/login.html", serverAddr)
	log.Println("---")
	log.Printf("  聊天 API Endpoint:      http://localhost%s/v1/chat/completions", serverAddr)
	log.Printf("  Gemini 原生格式 API:    http://localhost%s/v1beta/models/gemini-pro:generateContent", serverAddr)
	log.Printf("  访问 /v1 路径认证:     %s", tern(cfg.PollingAPIKey != "", "Bearer Token", "无"))
	log.Println("=========================================================")

	// 注意：服务端口是启动时绑定的，不能热重载。
	if err := router.Run(serverAddr); err != nil {
		log.Fatalf("启动 Gin 服务器失败: %v", err)
	}
}

// ... tern function
func tern(condition bool, trueVal, falseVal string) string {
	if condition {
		return trueVal
	}
	return falseVal
}
