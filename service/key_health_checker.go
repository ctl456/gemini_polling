// service/key_health_checker.go
package service

import (
	"context"
	"fmt"
	"gemini_polling/storage"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// 定义 Key 状态常量
const (
	KeyStatusOK          = iota // 0: Key 状态正常
	KeyStatusRateLimited        // 1: Key 被速率限制 (429)
	KeyStatusInvalid            // 2: Key 永久性无效 (其他 4xx)
)

// KeyHealthChecker 是一个统一的服务，负责定期检查所有 Key 的健康状况。
// 它会:
// 1. 定期扫描所有【已启用】的 Key:
//   - 如果遇到 429，则【临时禁用】。
//   - 如果遇到其他 4xx，则【永久禁用】。
//
// 2. 定期扫描所有【已禁用】的 Key:
//   - 如果 Key 恢复正常，则【重新启用】。
type KeyHealthChecker struct {
	keyStore     *storage.KeyStore
	genaiService *GenAIService
}

// NewKeyHealthChecker 创建一个新的健康检查器实例
func NewKeyHealthChecker(ks *storage.KeyStore, gs *GenAIService) *KeyHealthChecker {
	return &KeyHealthChecker{
		keyStore:     ks,
		genaiService: gs,
	}
}

// StartPeriodicChecks 启动一个后台 goroutine, 定期执行所有健康检查任务
func (c *KeyHealthChecker) StartPeriodicChecks(interval time.Duration) {
	log.Printf("统一 Key 健康检查服务已启动，检查间隔: %v", interval)
	go func() {
		// 立即执行一次，然后再按间隔执行
		c.RunAllChecks()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			c.RunAllChecks()
		}
	}()
}

// RunAllChecks 执行所有检查任务的入口点
func (c *KeyHealthChecker) RunAllChecks() {
	log.Println("================== [Key健康检查开始] ==================")
	c.checkEnabledKeys()
	time.Sleep(30 * time.Second) // 在两轮扫描之间稍作停顿
	c.checkDisabledKeys()
	log.Println("================== [Key健康检查结束] ==================")
}

// checkEnabledKeys 检查所有已启用的 Key
func (c *KeyHealthChecker) checkEnabledKeys() {
	log.Println("==> [健康检查] 开始扫描【已启用】的 Key...")
	keys, err := c.keyStore.GetAllEnabledKeys()
	if err != nil {
		log.Printf("[错误][健康检查] 扫描时无法获取已启用的 Key: %v", err)
		return
	}

	if len(keys) == 0 {
		log.Println("[健康检查] 没有已启用的 Key 可供扫描。")
		return
	}

	log.Printf("[健康检查] 发现 %d 个已启用的 Key，正在逐个检查...", len(keys))
	var rateLimitedCount, invalidCount int
	for _, key := range keys {
		// 如果 key 已经在内存中被临时禁用了，就不再重复检查
		if c.genaiService.isKeyRateLimited(key.ID) {
			continue
		}

		status, reason := c.checkKeyStatus(key.Key)
		switch status {
		case KeyStatusRateLimited:
			rateLimitedCount++
			log.Printf("  -> Key ID %d 检测到速率限制(429)，将【临时禁用���。原因: %s", key.ID, reason)
			c.genaiService.temporaryDisableKey(key.ID, "例行检查发现 429")

		case KeyStatusInvalid:
			invalidCount++
			log.Printf("  -> Key ID %d 检测为无效(4xx)，将【永久禁用】。原因: %s", key.ID, reason)
			c.keyStore.Disable(key.ID, "例行检查发现Key无效: "+reason)

		case KeyStatusOK:
			// 状态正常，无需日志，保持安静
		}

		time.Sleep(1 * time.Second)
	}

	log.Printf("<== 【已启用】Key 扫描完成。共检查 %d 个，临时禁用 %d 个，永久禁用 %d 个。", len(keys), rateLimitedCount, invalidCount)
}

// checkDisabledKeys 检查所有已禁用的 Key，看是否可以恢复
func (c *KeyHealthChecker) checkDisabledKeys() {
	log.Println("==> [健康检查] 开始扫描【已禁用】的 Key...")
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
	var recoveredCount int
	for _, key := range keys {
		status, _ := c.checkKeyStatus(key.Key)
		if status == KeyStatusOK {
			recoveredCount++
			log.Printf("  -> Key ID %d 验证通过，将【重新启用】。", key.ID)
			if err := c.keyStore.SetEnabled(key.ID, true); err != nil {
				log.Printf("  -> [错误] 启用 Key ID %d 失败: %v", key.ID, err)
			}
		}
		time.Sleep(1 * time.Second)
	}

	log.Printf("<== 【已禁用】Key 扫描完成。共检查 %d 个，重新启用了 %d 个。", len(keys), recoveredCount)
}

// checkKeyStatus 使用一个轻量级 API 调用来检查 key 的状态
// 返回值: (状态码, 原因)
func (c *KeyHealthChecker) checkKeyStatus(apiKey string) (int, string) {
	const url = "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:countTokens"
	reqBody := `{"contents":[{"parts":[{"text": "test"}]}]}`

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(reqBody))
	if err != nil {
		return KeyStatusOK, ""
	}

	req.Header.Set("x-goog-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.genaiService.httpClient.Do(req)
	if err != nil {
		return KeyStatusOK, ""
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
		return KeyStatusOK, ""
	}
}
