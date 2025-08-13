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
}

// NewKeyHealthChecker creates a new health checker instance.
func NewKeyHealthChecker(ks *storage.KeyStore, gs *GenAIService, kp *KeyPool, cm *config.Manager) *KeyHealthChecker {
	return &KeyHealthChecker{
		keyStore:      ks,
		genaiService:  gs,
		keyPool:       kp,
		configManager: cm,
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

// RunAllChecks is the entry point for executing all check tasks.
func (c *KeyHealthChecker) RunAllChecks() {
	log.Println("================== [Key健康检查开始] ==================")
	c.checkEnabledKeys()
	c.checkDisabledKeys()
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

// checkKeyStatus uses a lightweight API call to check the status of a key.
func (c *KeyHealthChecker) checkKeyStatus(apiKey string) (int, string) {
	// 使用 'POST models:countTokens' 请求作为健康检查，因为它能更准确地反映生成类API的速率限制状态。
	// 我们使用 gemini-pro，因为它是一个常用模型。
	const url = "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:countTokens"
	const requestBody = `{"contents":[{"parts":[{"text":"hi"}]}]}`

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