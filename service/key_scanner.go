package service

import (
	"gemini_polling/storage"
	"log"
	"time"
)

// KeyScanner 负责定期或手动扫描和验证 API Key 的有效性
type KeyScanner struct {
	keyStore     *storage.KeyStore
	genaiService *GenAIService
}

// NewKeyScanner 创建一个新的 KeyScanner 实例
func NewKeyScanner(ks *storage.KeyStore, gs *GenAIService) *KeyScanner {
	return &KeyScanner{
		keyStore:     ks,
		genaiService: gs,
	}
}

// StartPeriodicScan 启动一个后台 goroutine，定期扫描所有启用的 key
// interval: 扫描的间隔时间, e.g., 24 * time.Hour
func (s *KeyScanner) StartPeriodicScan(interval time.Duration) {
	log.Printf("自动 Key 验证任务已启动，扫描间隔: %v", interval)

	go func() {
		// 立即执行一次，不等第一个 Ticker
		s.ScanAllEnabledKeys()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			s.ScanAllEnabledKeys()
		}
	}()
}

// ScanAllEnabledKeys 是执行扫描的核心方法
func (s *KeyScanner) ScanAllEnabledKeys() {
	log.Println("==> 开始执行所有启用 Key 的有效性扫描...")
	keys, err := s.keyStore.GetAllEnabledKeys()
	if err != nil {
		log.Printf("[错误] 扫描时无法获取已启用的 Key: %v", err)
		return
	}

	if len(keys) == 0 {
		log.Println("数据库中没有已启用的 Key，跳过扫描。")
		return
	}

	log.Printf("发现 %d 个已启用的 Key，正在逐个验证...", len(keys))
	var invalidCount int
	for _, key := range keys {
		isValid, reason := s.genaiService.ValidateAPIKey(key.Key)
		if !isValid {
			invalidCount++
			log.Printf("  -> Key ID %d (尾号...%s) 验证失败，正在禁用。原因: %s", key.ID, key.Key[len(key.Key)-4:], reason)
			s.keyStore.Disable(key.ID, "自动扫描发现失效: "+reason)
		} else {
			log.Printf("  -> Key ID %d 验证通过。", key.ID)
		}
		// 在每次请求之间稍作停顿，避免触发上游 API 的速率限制
		time.Sleep(500 * time.Millisecond)
	}

	log.Printf("<== 扫描完成。共检查 %d 个 Key，发现并禁用了 %d 个无效 Key。", len(keys), invalidCount)
}
