// service/key_health_checker.go
package service

import (
	"context"
	"fmt"
	"gemini_polling/config"
	"gemini_polling/model"
	"gemini_polling/storage"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 定义 Key 状态常量
const (
	KeyStatusOK          = iota // 0: Key 状态正常
	KeyStatusRateLimited        // 1: Key 被速率限制 (429)
	KeyStatusInvalid            // 2: Key 永久性无效 (其他 4xx)
)

// checkResult holds the outcome of a single key health check.
type checkResult struct {
	Key    model.APIKey
	Status int
	Reason string
}

// KeyHealthChecker is a unified service that concurrently checks the health of all API keys.
type KeyHealthChecker struct {
	keyStore      *storage.KeyStore
	genaiService  *GenAIService
	keyPool       *KeyPool
	configManager *config.Manager
	progress      *CheckProgress
}

// CheckProgress 用于跟踪健康检查进度
type CheckProgress struct {
	mu                sync.Mutex
	TotalKeys         int
	ProcessedKeys     int32
	EnabledKeys       int
	DisabledKeys      int
	StartTime         time.Time
	LastUpdate        time.Time
	RateLimitedCount  int32
	InvalidCount      int32
	RecoveredCount    int32
	CurrentCheckType  string
	IsActive          bool
}

// ProgressInfo 用于向前端返回进度信息
type ProgressInfo struct {
	TotalKeys        int     `json:"total_keys"`
	ProcessedKeys    int     `json:"processed_keys"`
	Progress         float64 `json:"progress"`
	EnabledKeys      int     `json:"enabled_keys"`
	DisabledKeys     int     `json:"disabled_keys"`
	RateLimitedCount int     `json:"rate_limited_count"`
	InvalidCount     int     `json:"invalid_count"`
	RecoveredCount   int     `json:"recovered_count"`
	CurrentCheckType string  `json:"current_check_type"`
	ElapsedTime      string  `json:"elapsed_time"`
	ETA              string  `json:"eta"`
	IsActive         bool    `json:"is_active"`
}

// NewKeyHealthChecker creates a new health checker instance.
func NewKeyHealthChecker(ks *storage.KeyStore, gs *GenAIService, kp *KeyPool, cm *config.Manager) *KeyHealthChecker {
	return &KeyHealthChecker{
		keyStore:      ks,
		genaiService:  gs,
		keyPool:       kp,
		configManager: cm,
		progress:      &CheckProgress{},
	}
}

// StartPeriodicChecks starts a background goroutine to periodically run all health checks.
func (c *KeyHealthChecker) StartPeriodicChecks(interval time.Duration) {
	log.Printf("统一 Key 健康检查服务已启动，检查间隔: %v", interval)
	go func() {
		time.Sleep(10 * time.Second) // Initial delay before the first run
		c.RunAllChecks()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			c.RunAllChecks()
		}
	}()
}

// GetProgress 获取当前检查进度
func (c *KeyHealthChecker) GetProgress() ProgressInfo {
	c.progress.mu.Lock()
	defer c.progress.mu.Unlock()

	info := ProgressInfo{
		TotalKeys:        c.progress.TotalKeys,
		ProcessedKeys:    int(atomic.LoadInt32(&c.progress.ProcessedKeys)),
		EnabledKeys:      c.progress.EnabledKeys,
		DisabledKeys:     c.progress.DisabledKeys,
		RateLimitedCount: int(atomic.LoadInt32(&c.progress.RateLimitedCount)),
		InvalidCount:     int(atomic.LoadInt32(&c.progress.InvalidCount)),
		RecoveredCount:   int(atomic.LoadInt32(&c.progress.RecoveredCount)),
		CurrentCheckType: c.progress.CurrentCheckType,
		IsActive:         c.progress.IsActive,
	}

	if c.progress.IsActive {
		info.ElapsedTime = time.Since(c.progress.StartTime).Round(time.Second).String()
		
		// 计算 ETA
		if info.ProcessedKeys > 0 {
			avgTimePerKey := time.Since(c.progress.StartTime).Seconds() / float64(info.ProcessedKeys)
			remainingKeys := c.progress.TotalKeys - info.ProcessedKeys
			etaSeconds := avgTimePerKey * float64(remainingKeys)
			info.ETA = time.Duration(etaSeconds * float64(time.Second)).Round(time.Second).String()
		}
		
		// 计算进度百分比
		if c.progress.TotalKeys > 0 {
			info.Progress = float64(info.ProcessedKeys) / float64(c.progress.TotalKeys) * 100
		}
	}

	return info
}

// startCheck 开始检查并初始化进度
func (c *KeyHealthChecker) startCheck(checkType string, totalKeys int) {
	c.progress.mu.Lock()
	defer c.progress.mu.Unlock()
	
	c.progress.TotalKeys = totalKeys
	c.progress.ProcessedKeys = 0
	c.progress.EnabledKeys = 0
	c.progress.DisabledKeys = 0
	atomic.StoreInt32(&c.progress.RateLimitedCount, 0)
	atomic.StoreInt32(&c.progress.InvalidCount, 0)
	atomic.StoreInt32(&c.progress.RecoveredCount, 0)
	c.progress.CurrentCheckType = checkType
	c.progress.StartTime = time.Now()
	c.progress.LastUpdate = time.Now()
	c.progress.IsActive = true
}

// updateProgress 更新检查进度
func (c *KeyHealthChecker) updateProgress() {
	atomic.AddInt32(&c.progress.ProcessedKeys, 1)
	c.progress.mu.Lock()
	c.progress.LastUpdate = time.Now()
	c.progress.mu.Unlock()
}

// finishCheck 完成检查
func (c *KeyHealthChecker) finishCheck() {
	c.progress.mu.Lock()
	defer c.progress.mu.Unlock()
	c.progress.IsActive = false
}

// RunAllChecks is the entry point for executing all check tasks.
func (c *KeyHealthChecker) RunAllChecks() {
	log.Println("================== [Key健康检查开始] ==================")
	// 获取所有 Key 数量用于进度显示
	enabledKeys, _ := c.keyStore.GetAllEnabledKeys()
	disabledKeys, _ := c.keyStore.GetAllDisabledKeys()
	totalKeys := len(enabledKeys) + len(disabledKeys)
	
	if totalKeys == 0 {
		log.Println("[健康检查] 没有 Key 可供扫描。")
		return
	}
	
	log.Printf("[健康检查] 开始扫描所有 %d 个 Key（启用: %d, 禁用: %d）", totalKeys, len(enabledKeys), len(disabledKeys))
	
	if len(enabledKeys) > 0 {
		c.checkEnabledKeysWithProgress(enabledKeys)
	}
	if len(disabledKeys) > 0 {
		c.checkDisabledKeysWithProgress(disabledKeys)
	}
	
	log.Println("================== [Key健康检查结束] ==================")
}

// RunAllKeysCheckWithProgress 带进度显示的完整检查
func (c *KeyHealthChecker) RunAllKeysCheckWithProgress() {
	// 获取所有 Key 数量用于进度显示
	enabledKeys, _ := c.keyStore.GetAllEnabledKeys()
	disabledKeys, _ := c.keyStore.GetAllDisabledKeys()
	totalKeys := len(enabledKeys) + len(disabledKeys)
	
	if totalKeys == 0 {
		log.Println("[健康检查] 没有 Key 可供扫描。")
		return
	}
	
	log.Printf("[健康检查] 开始扫描所有 %d 个 Key（启用: %d, 禁用: %d）", totalKeys, len(enabledKeys), len(disabledKeys))
	
	c.checkEnabledKeysWithProgress(enabledKeys)
	c.checkDisabledKeysWithProgress(disabledKeys)
	
	log.Println("================== [Key健康检查结束] ==================")
}

// runChecksConcurrently is the core worker pool for checking keys.
func (c *KeyHealthChecker) runChecksConcurrently(keys []model.APIKey, checkType string) {
	concurrency := c.configManager.Get().HealthCheckConcurrency
	if concurrency <= 0 {
		concurrency = 10 // Fallback to a safe default
	}

	jobs := make(chan model.APIKey, len(keys))
	results := make(chan checkResult, len(keys))

	var wg sync.WaitGroup
	// Start workers
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for key := range jobs {
				// For enabled keys, don't re-check if they are already in the cooldown map.
				if checkType == "enabled" {
					if _, onCooldown := c.keyPool.cooldownKeys.Load(key.ID); onCooldown {
						continue
					}
				}
				status, reason := c.checkKeyStatus(key.Key)
				results <- checkResult{Key: key, Status: status, Reason: reason}
			}
		}(i)
	}

	// Feed jobs
	for _, key := range keys {
		jobs <- key
	}
	close(jobs)

	// Wait for all workers to finish
	wg.Wait()
	close(results)

	// Process results
	c.processResults(results, checkType)
}

// processResults consumes the results from the concurrent checks and takes action.
func (c *KeyHealthChecker) processResults(results chan checkResult, checkType string) {
	var rateLimitedCount, invalidCount, recoveredCount int

	for result := range results {
		switch checkType {
		case "enabled":
			switch result.Status {
			case KeyStatusRateLimited:
				rateLimitedCount++
				log.Printf("  -> [启用Key检查] Key ID %d 检测到速率限制(429)，将进入冷却。原因: %s", result.Key.ID, result.Reason)
				// Use the keypool's method to handle cooldown
				c.keyPool.ReturnKey(&result.Key, true)
			case KeyStatusInvalid:
				invalidCount++
				log.Printf("  -> [启用Key检查] Key ID %d 检测为无效(4xx)，将【永久禁用】。原因: %s", result.Key.ID, result.Reason)
				c.keyStore.Disable(result.Key.ID, "例行检查发现Key无效: "+result.Reason)
			}
		case "disabled":
			if result.Status == KeyStatusOK {
				recoveredCount++
				log.Printf("  -> [禁用Key检查] Key ID %d 验证通过，将【重新启用】。", result.Key.ID)
				if err := c.keyStore.SetEnabled(result.Key.ID, true); err != nil {
					log.Printf("  -> [错误] 启用 Key ID %d 失败: %v", result.Key.ID, err)
				}
			}
		}
	}

	switch checkType {
	case "enabled":
		log.Printf("<== 【已启用】Key 扫描完成。共处理 %d 个结果，临时禁用 %d 个，永久禁用 %d 个。", cap(results), rateLimitedCount, invalidCount)
	case "disabled":
		log.Printf("<== 【已禁用】Key 扫描完成。共处理 %d 个结果，重新启用了 %d 个。", cap(results), recoveredCount)
	}
}

// checkEnabledKeys checks all enabled keys.
func (c *KeyHealthChecker) checkEnabledKeys() {
	log.Println("==> [健康检查] 开始并发扫描【已启用】的 Key...")
	keys, err := c.keyStore.GetAllEnabledKeys()
	if err != nil {
		log.Printf("[错误][健康检查] 扫描时无法获取已启用的 Key: %v", err)
		return
	}

	if len(keys) == 0 {
		log.Println("[健康检查] 没有已启用的 Key 可供扫描。")
		return
	}

	log.Printf("[健康检查] 发现 %d 个已启用的 Key，开始检查...", len(keys))
	c.runChecksConcurrently(keys, "enabled")
}

// checkEnabledKeysWithProgress 带进度显示的已启用 Key 检查
func (c *KeyHealthChecker) checkEnabledKeysWithProgress(keys []model.APIKey) {
	if len(keys) == 0 {
		log.Println("[健康检查] 没有已启用的 Key 可供扫描。")
		return
	}

	c.startCheck("已启用 Key", len(keys))
	c.progress.mu.Lock()
	c.progress.EnabledKeys = len(keys)
	c.progress.mu.Unlock()
	
	// 根据数量显示提示信息
	displayInterval := 1
	if len(keys) > 1000 {
		displayInterval = 100
		log.Printf("[健康检查] 发现 %d 个已启用的 Key，开始检查... (每 %d 个显示一次进度)", len(keys), displayInterval)
	} else if len(keys) > 100 {
		displayInterval = 50
		log.Printf("[健康检查] 发现 %d 个已启用的 Key，开始检查... (每 %d 个显示一次进度)", len(keys), displayInterval)
	} else if len(keys) > 50 {
		displayInterval = 10
		log.Printf("[健康检查] 发现 %d 个已启用的 Key，开始检查... (每 %d 个显示一次进度)", len(keys), displayInterval)
	} else {
		log.Printf("[健康检查] 发现 %d 个已启用的 Key，开始检查... (实时显示进度)", len(keys))
	}
	
	c.runChecksConcurrentlyWithProgress(keys, "enabled")
	c.finishCheck()
}

// checkDisabledKeys checks all disabled keys to see if they can be re-enabled.
func (c *KeyHealthChecker) checkDisabledKeys() {
	log.Println("==> [健康检查] 开始并发扫描【已禁用】的 Key...")
	keys, err := c.keyStore.GetAllDisabledKeys()
	if err != nil {
		log.Printf("[错误][健康检查] 扫描时无法获取已禁用的 Key: %v", err)
		return
	}

	if len(keys) == 0 {
		log.Println("[健康检查] 没有已禁用的 Key 可供扫描。")
		return
	}

	log.Printf("[健康检查] 发现 %d 个已禁用的 Key，正在尝试恢复...", len(keys))
	c.runChecksConcurrently(keys, "disabled")
}

// checkDisabledKeysWithProgress 带进度显示的已禁用 Key 检查
func (c *KeyHealthChecker) checkDisabledKeysWithProgress(keys []model.APIKey) {
	if len(keys) == 0 {
		log.Println("[健康检查] 没有已禁用的 Key 可供扫描。")
		return
	}

	c.startCheck("已禁用 Key", len(keys))
	c.progress.mu.Lock()
	c.progress.DisabledKeys = len(keys)
	c.progress.mu.Unlock()
	
	// 根据数量显示提示信息
	displayInterval := 1
	if len(keys) > 1000 {
		displayInterval = 100
		log.Printf("[健康检查] 发现 %d 个已禁用的 Key，正在尝试恢复... (每 %d 个显示一次进度)", len(keys), displayInterval)
	} else if len(keys) > 100 {
		displayInterval = 50
		log.Printf("[健康检查] 发现 %d 个已禁用的 Key，正在尝试恢复... (每 %d 个显示一次进度)", len(keys), displayInterval)
	} else if len(keys) > 50 {
		displayInterval = 10
		log.Printf("[健康检查] 发现 %d 个已禁用的 Key，正在尝试恢复... (每 %d 个显示一次进度)", len(keys), displayInterval)
	} else {
		log.Printf("[健康检查] 发现 %d 个已禁用的 Key，正在尝试恢复... (实时显示进度)", len(keys))
	}
	
	c.runChecksConcurrentlyWithProgress(keys, "disabled")
	c.finishCheck()
}

// runChecksConcurrentlyWithProgress 带进度显示的并发检查
func (c *KeyHealthChecker) runChecksConcurrentlyWithProgress(keys []model.APIKey, checkType string) {
	concurrency := c.configManager.Get().HealthCheckConcurrency
	if concurrency <= 0 {
		concurrency = 10 // Fallback to a safe default
	}

	jobs := make(chan model.APIKey, len(keys))
	results := make(chan checkResult, len(keys))

	var wg sync.WaitGroup
	// Start workers
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for key := range jobs {
				// For enabled keys, don't re-check if they are already in the cooldown map.
				if checkType == "enabled" {
					if _, onCooldown := c.keyPool.cooldownKeys.Load(key.ID); onCooldown {
						c.updateProgress()
						continue
					}
				}
				status, reason := c.checkKeyStatus(key.Key)
				results <- checkResult{Key: key, Status: status, Reason: reason}
				c.updateProgress()
				
				// 定期打印进度 - 根据总数动态调整显示频率
				progress := c.GetProgress()
				displayInterval := 1
				if progress.TotalKeys > 1000 {
					displayInterval = 100
				} else if progress.TotalKeys > 100 {
					displayInterval = 50
				} else if progress.TotalKeys > 50 {
					displayInterval = 10
				}
				
				if progress.ProcessedKeys%displayInterval == 0 || progress.ProcessedKeys == progress.TotalKeys {
					// 计算速率 - 使用检查开始时间
					c.progress.mu.Lock()
					elapsed := time.Since(c.progress.StartTime).Seconds()
					c.progress.mu.Unlock()
					rate := float64(progress.ProcessedKeys) / elapsed
					rateStr := fmt.Sprintf("%.1f keys/s", rate)
					
					log.Printf("[进度] %s: %d/%d (%.1f%%) - 速率: %s - 已用: %s, 预计剩余: %s", 
						progress.CurrentCheckType,
						progress.ProcessedKeys, 
						progress.TotalKeys, 
						progress.Progress,
						rateStr,
						progress.ElapsedTime,
						progress.ETA)
				}
			}
		}(i)
	}

	// Feed jobs
	for _, key := range keys {
		jobs <- key
	}
	close(jobs)

	// Wait for all workers to finish
	wg.Wait()
	close(results)

	// Process results
	c.processResults(results, checkType)
}

// checkKeyStatus uses a lightweight API call to check the status of a key.
func (c *KeyHealthChecker) checkKeyStatus(apiKey string) (int, string) {
	// 使用 'POST models:countTokens' 请求作为健康检查，因为它能更准确地反映生成类API的速率限制状态。
	// 我们使用 gemini-2.5-pro，因为它是一个常用模型。
	const url = "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent"
	const requestBody = `{"contents":[{"parts":[{"text":"Explain how AI works in a few words"}]}]}`

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(requestBody))
	if err != nil {
		// 这是一个本地错误，不是密钥状态问题。
		return KeyStatusOK, "Failed to create request: " + err.Error()
	}

	req.Header.Set("x-goog-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.genaiService.httpClient.Do(req)
	if err != nil {
		// 网络错误，暂时假定密钥正常，可能只是临时的网络问题。
		return KeyStatusOK, "Request failed: " + err.Error()
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return KeyStatusOK, ""
	case http.StatusTooManyRequests:
		body, _ := io.ReadAll(resp.Body)
		return KeyStatusRateLimited, fmt.Sprintf("API返回429: %s", string(body))
	default:
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			body, _ := io.ReadAll(resp.Body)
			return KeyStatusInvalid, fmt.Sprintf("API返回%d: %s", resp.StatusCode, string(body))
		}
		// 对于 5xx 或其他错误，我们假定是临时的服务器端问题，不惩罚密钥。
		return KeyStatusOK, ""
	}
}
