package handler

import (
	"gemini_polling/config"
	"gemini_polling/logger"
	"gemini_polling/service"
	"gemini_polling/storage"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

type KeyHandler struct {
	store         *storage.KeyStore
	genaiService  *service.GenAIService
	configManager *config.Manager
	healthChecker *service.KeyHealthChecker
	keyPool       *service.KeyPool
}

// +修改: 更新 NewKeyHandler 的签名
func NewKeyHandler(store *storage.KeyStore, genaiService *service.GenAIService, manager *config.Manager, checker *service.KeyHealthChecker, keyPool *service.KeyPool) *KeyHandler {
	return &KeyHandler{
		store:         store,
		genaiService:  genaiService,
		configManager: manager,
		healthChecker: checker,
		keyPool:       keyPool,
	}
}

// Login 验证管理员登录
func (h *KeyHandler) Login(c *gin.Context) {
	var json struct {
		APIKey string `json:"api_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 从管理器获取最新的 Admin Key
	adminAPIKey := h.configManager.Get().AdminAPIKey
	if json.APIKey != adminAPIKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid admin API key"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Login successful"})
}

// AddKey 添加单个 Key
// AddKey 添加单个 Key, 并立即进行验证
func (h *KeyHandler) AddKey(c *gin.Context) {
	var json struct {
		APIKey string `json:"api_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 1. 先添加到数据库
	key, err := h.store.Add(json.APIKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add key: " + err.Error()})
		return
	}
	// 2. 立即进行验证
	logger.Info("新 Key (ID: %d) 已添加，正在进行即时验证...", key.ID)
	isValid, reason := h.genaiService.ValidateAPIKey(key.Key)
	if !isValid {
		logger.Warn("新 Key (ID: %d) 未通过验证，已自动禁用。原因: %s", key.ID, reason)
		h.store.Disable(key.ID, "添加时验证失败: "+reason)
		// 更新 key 对象的状态以返回给前端
		key.Enabled = false
	} else {
		logger.Info("新 Key (ID: %d) 验证通过。", key.ID)
	}
	c.JSON(http.StatusOK, key)
}

// +新增: ScanAllKeysHandler 用于处理手动扫描请求
func (h *KeyHandler) ScanAllKeysHandler(c *gin.Context) {
	// 在后台运行扫描，立即返回响应
	go h.healthChecker.RunAllChecks()
	c.JSON(http.StatusOK, gin.H{
		"message": "已启动对所有 Key（包括已启用和已禁用）的后台健康检查任务，请稍后查看日志。",
	})
}

// +新增: ScanAllKeysWithProgressHandler 用于处理带进度显示的手动扫描请求
func (h *KeyHandler) ScanAllKeysWithProgressHandler(c *gin.Context) {
	// 在后台运行带进度显示的扫描，立即返回响应
	go h.healthChecker.RunAllKeysCheckWithProgress()
	c.JSON(http.StatusOK, gin.H{
		"message": "已启动带进度显示的健康检查任务，可通过进度API查看检查进度。",
	})
}

// +新增: GetHealthCheckProgressHandler 获取健康检查进度
func (h *KeyHandler) GetHealthCheckProgressHandler(c *gin.Context) {
	progress := h.healthChecker.GetProgress()
	c.JSON(http.StatusOK, progress)
}

// BatchAddKeys 批量添加 Keys
func (h *KeyHandler) BatchAddKeys(c *gin.Context) {
	var json struct {
		Keys []string `json:"keys" binding:"required"`
	}
	if err := c.ShouldBindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// 前端已经做了一次去重，后端逻辑直接处理即可
	if len(json.Keys) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"message": "没有提供有效的 Key",
			"added":   0,
			"skipped": 0,
		})
		return
	}
	// 调用新的、更智能的存储方法
	added, skipped, err := h.store.AddMultiple(json.Keys)
	if err != nil {
		logger.Error("批量添加 Keys 失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量添加时发生数据库错误: " + err.Error()})
		return
	}
	// 返回新的响应格式
	c.JSON(http.StatusOK, gin.H{
		"message": "批量添加完成",
		"added":   added,
		"skipped": skipped,
	})
}

// CheckSingleKey 校验单个指定的 Key
func (h *KeyHandler) CheckSingleKey(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID format"})
		return
	}
	key, err := h.store.FindByID(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Key not found"})
		return
	}
	isValid, reason := h.genaiService.ValidateAPIKey(key.Key)
	// 如果校验结果与当前状态不符，则更新数据库
	if key.Enabled != isValid {
		logger.Info("Key ID %d status changed to %v based on validation. Reason: %s", id, isValid, reason)
		h.store.SetEnabled(uint(id), isValid)
	}
	c.JSON(http.StatusOK, gin.H{
		"is_valid": isValid,
		"reason":   reason,
	})
}

// ListKeys 列出所有 Key (支持分页和过滤)
func (h *KeyHandler) ListKeys(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	status := c.DefaultQuery("status", "enabled") // 默认显示启用的
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	keys, total, err := h.store.ListKeys(page, pageSize, status)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list keys: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"keys":        keys,
		"total_count": total,
		"page":        page,
		"page_size":   pageSize,
	})
}

// BatchDeleteKeys 批量删除 Keys
func (h *KeyHandler) BatchDeleteKeys(c *gin.Context) {
	var json struct {
		IDs []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	deletedCount, err := h.store.BatchDelete(json.IDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete keys: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Batch delete complete",
		"deleted": deletedCount,
	})
}

// DeleteAllDisabledKeys 一键删除所有已禁用的key
func (h *KeyHandler) DeleteAllDisabledKeys(c *gin.Context) {
	deletedCount, err := h.store.DeleteAllDisabledKeys()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete disabled keys: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "All disabled keys deleted successfully",
		"deleted": deletedCount,
	})
}

func (h *KeyHandler) setKeyStatus(c *gin.Context, enabled bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID format"})
		return
	}

	if err := h.store.SetEnabled(uint(id), enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update key status: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "id": id, "enabled": enabled})
}

func (h *KeyHandler) ActivateKey(c *gin.Context) {
	h.setKeyStatus(c, true)
}

func (h *KeyHandler) DeactivateKey(c *gin.Context) {
	h.setKeyStatus(c, false)
}

func (h *KeyHandler) DeleteKey(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID format"})
		return
	}

	if err := h.store.Delete(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete key: " + err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// ListBannedKeys 列出所有被临时禁用的 Key
func (h *KeyHandler) ListBannedKeys(c *gin.Context) {
	// 1. Get pagination parameters from query
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}

	// 2. Get all banned keys from the pool
	bannedKeys, err := h.keyPool.GetBannedKeysInfo()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取临时禁用列表失败: " + err.Error()})
		return
	}

	// 3. Manually paginate the results
	total := len(bannedKeys)
	start := (page - 1) * pageSize
	end := start + pageSize

	// Bounds check for slicing
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	var paginatedKeys []service.BannedKeyInfo
	if start < end {
		paginatedKeys = bannedKeys[start:end]
	} else {
		paginatedKeys = []service.BannedKeyInfo{}
	}

	// 4. Return paginated response
	c.JSON(http.StatusOK, gin.H{
		"keys":        paginatedKeys,
		"total_count": total,
		"page":        page,
		"page_size":   pageSize,
	})
}

// GetKeyStats returns statistics about the keys.
func (h *KeyHandler) GetKeyStats(c *gin.Context) {
	enabledCount, disabledCount, err := h.store.GetKeyCounts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get key counts: " + err.Error()})
		return
	}

	bannedCount := h.keyPool.GetBannedKeyCount()

	c.JSON(http.StatusOK, gin.H{
		"enabled_count":  enabledCount,
		"disabled_count": disabledCount,
		"banned_count":   bannedCount,
	})
}