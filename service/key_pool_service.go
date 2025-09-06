// service/key_pool_service.go
package service

import (
	"errors"
	"gemini_polling/config"
	"gemini_polling/logger"
	"gemini_polling/model"
	"gemini_polling/storage"
	"math/rand"
	"sync"
	"time"
)

// ErrNoAvailableKeys is returned when no keys are available in the pool.
var ErrNoAvailableKeys = errors.New("key pool: no available keys")

// KeyPool manages a pool of API keys in memory for high-performance access.
type KeyPool struct {
	keyStore      *storage.KeyStore
	configManager *config.Manager

	mu            sync.RWMutex
	availableKeys chan *model.APIKey // Channel of available keys, acts as the pool
	allKeys       map[uint]*model.APIKey // Holds all known enabled keys for quick lookup

	// A separate map to track keys that are temporarily on cooldown (e.g., due to 429).
	// This prevents them from being added back to the available pool immediately.
	cooldownKeys sync.Map // map[uint]time.Time
	
	// 新增：智能key管理
	keyStats      map[uint]*KeyStats
}

// KeyStats 用于跟踪key的统计信息
type KeyStats struct {
	LastUsedAt      time.Time
	Last429At       time.Time
	SuccessCount    int
	FailureCount    int
	RateLimitCount  int
	HealthScore     int
	NextAvailableAt time.Time
	IsOnCooldown    bool
}

// NewKeyPool creates a new KeyPool service.
func NewKeyPool(keyStore *storage.KeyStore, configManager *config.Manager) *KeyPool {
	return &KeyPool{
		keyStore:      keyStore,
		configManager: configManager,
		allKeys:       make(map[uint]*model.APIKey),
		keyStats:      make(map[uint]*KeyStats),
	}
}

// Start initializes the pool and begins periodic refresh operations.
func (p *KeyPool) Start(refreshInterval time.Duration) {
	logger.Infoln("启动内存 Key 池服务...")
	p.initialLoad()

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			logger.Infoln("[Key Pool] 定时刷新 Key 列表...")
			p.refresh()
		}
	}()
}

// initialLoad performs the first load of keys from the database.
func (p *KeyPool) initialLoad() {
	keys, err := p.keyStore.GetAllEnabledKeys()
	if err != nil {
		logger.Error("[错误] Key 池初始化加载失败: %v", err)
		// Even if it fails, we create the channel to avoid nil panics.
		p.availableKeys = make(chan *model.APIKey, 1) // Default size
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Create a channel with enough capacity for all keys.
	p.availableKeys = make(chan *model.APIKey, len(keys))
	p.allKeys = make(map[uint]*model.APIKey, len(keys))

	for i := range keys {
		key := keys[i] // Create a new variable for the pointer
		p.allKeys[key.ID] = &key
		p.availableKeys <- &key
	}

	logger.Info("Key 池初始化成功，加载了 %d 个可用的 Key。", len(keys))
}

// refresh reloads keys from the database and updates the pool.
func (p *KeyPool) refresh() {
	dbKeys, err := p.keyStore.GetAllEnabledKeys()
	if err != nil {
		logger.Error("[错误] Key 池刷新失败: %v", err)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	newKeysMap := make(map[uint]*model.APIKey, len(dbKeys))
	for i := range dbKeys {
		key := dbKeys[i]
		newKeysMap[key.ID] = &key
	}

	// Update allKeys map
	p.allKeys = newKeysMap

	// Rebuild the availableKeys channel
	// 1. Drain the current channel of any remaining keys
	close(p.availableKeys)
	// 2. Create a new channel
	p.availableKeys = make(chan *model.APIKey, len(p.allKeys))
	// 3. Fill it with the new set of keys, respecting cooldowns
	refreshedCount := 0
	for _, key := range p.allKeys {
		if _, onCooldown := p.cooldownKeys.Load(key.ID); !onCooldown {
			p.availableKeys <- key
			refreshedCount++
		}
	}

	logger.Info("[Key Pool] 刷新完成。数据库中共有 %d 个启用 Key，当前可用 %d 个。", len(p.allKeys), refreshedCount)
}

// GetKey retrieves an available key from the pool using intelligent selection.
func (p *KeyPool) GetKey() (*model.APIKey, error) {
	// 首先尝试获取智能选择的key
	if key := p.getBestAvailableKey(); key != nil {
		return key, nil
	}
	
	// 如果没有可用key，等待并重试
	select {
	case key, ok := <-p.availableKeys:
		if !ok {
			return nil, errors.New("key channel is closed")
		}
		return key, nil
	case <-time.After(30 * time.Second):
		return nil, ErrNoAvailableKeys
	}
}

// getBestAvailableKey 使用智能算法选择最佳可用key
func (p *KeyPool) getBestAvailableKey() *model.APIKey {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	var availableKeys []*model.APIKey
	now := time.Now()
	cfg := p.configManager.Get()
	
	// 收集所有可用的key
	for _, key := range p.allKeys {
		stats := p.keyStats[key.ID]
		
		// 检查key是否可用
		if stats != nil {
			// 如果key在冷却中且未到时间，跳过
			if stats.IsOnCooldown && now.Before(stats.NextAvailableAt) {
				continue
			}
			
			// 如果key健康分数太低，跳过
			if stats.HealthScore < cfg.MinHealthScore {
				continue
			}
			
			// 如果429次数过多，跳过
			if stats.RateLimitCount > cfg.Max429Count {
				continue
			}
			
			// 如果最近有429记录，根据时间判断是否跳过
			if !stats.Last429At.IsZero() {
				timeSince429 := now.Sub(stats.Last429At)
				// 如果429发生在最近1小时内，降低选择概率
				if timeSince429 < time.Hour {
					if rand.Float32() > 0.1 { // 90%概率跳过
						continue
					}
				}
			}
		}
		
		availableKeys = append(availableKeys, key)
	}
	
	if len(availableKeys) == 0 {
		return nil
	}
	
	// 根据健康分数加权随机选择
	return p.selectKeyByWeight(availableKeys)
}

// selectKeyByWeight 根据健康分数加权随机选择key
func (p *KeyPool) selectKeyByWeight(keys []*model.APIKey) *model.APIKey {
	if len(keys) == 0 {
		return nil
	}
	
	if len(keys) == 1 {
		return keys[0]
	}
	
	// 计算总权重
	totalWeight := 0
	weights := make([]int, len(keys))
	for i, key := range keys {
		stats := p.keyStats[key.ID]
		weight := 50 // 基础权重
		
		if stats != nil {
			weight = stats.HealthScore
			// 根据最近的成功率调整权重
			if stats.SuccessCount+stats.FailureCount > 0 {
				successRate := float64(stats.SuccessCount) / float64(stats.SuccessCount+stats.FailureCount)
				weight = int(float64(weight) * successRate)
			}
		}
		
		// 确保权重不为0
		if weight < 1 {
			weight = 1
		}
		
		weights[i] = weight
		totalWeight += weight
	}
	
	// 加权随机选择
	randomWeight := rand.Intn(totalWeight)
	currentWeight := 0
	
	for i, weight := range weights {
		currentWeight += weight
		if randomWeight < currentWeight {
			return keys[i]
		}
	}
	
	return keys[len(keys)-1]
}

// ReturnKey returns a key to the pool, optionally putting it on cooldown with intelligent strategy.
func (p *KeyPool) ReturnKey(key *model.APIKey, isRateLimited bool) {
	if key == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	
	// 确保stats存在
	if _, exists := p.keyStats[key.ID]; !exists {
		p.keyStats[key.ID] = &KeyStats{
			HealthScore: 100,
		}
	}
	
	stats := p.keyStats[key.ID]
	stats.LastUsedAt = time.Now()

	if isRateLimited {
		// 智能冷却策略
		p.handleRateLimit(key, stats)
	} else {
		// 成功使用，增加健康分数
		p.handleSuccess(key, stats)
	}

	// 如果key可用，返回到池中
	if !stats.IsOnCooldown && stats.HealthScore >= 30 {
		select {
		case p.availableKeys <- key:
		default:
			logger.Warn("[Key Pool] Key ID %d 返回失败，通道已满。", key.ID)
		}
	}
}

// handleRateLimit 处理429限流
func (p *KeyPool) handleRateLimit(key *model.APIKey, stats *KeyStats) {
	stats.Last429At = time.Now()
	stats.RateLimitCount++
	stats.FailureCount++
	
	// 计算智能冷却时间
	cooldownDuration := p.calculateSmartCooldown(stats)
	stats.NextAvailableAt = time.Now().Add(cooldownDuration)
	stats.IsOnCooldown = true
	
	// 降低健康分数
	p.decreaseHealthScore(stats, 20)
	
	logger.Info("[Key Pool] Key ID %d 遭遇429，智能冷却 %v (健康分数: %d, 429次数: %d)", 
		key.ID, cooldownDuration, stats.HealthScore, stats.RateLimitCount)
	
	// 异步恢复key
	go p.scheduleKeyRecovery(key.ID, cooldownDuration)
}

// handleSuccess 处理成功使用
func (p *KeyPool) handleSuccess(key *model.APIKey, stats *KeyStats) {
	stats.SuccessCount++
	
	// 增加健康分数，但不超过100
	cfg := p.configManager.Get()
	if stats.HealthScore < 100 {
		p.increaseHealthScore(stats, cfg.RecoveryBonus)
	}
	
	// 如果之前在冷却中，重置状态
	if stats.IsOnCooldown {
		stats.IsOnCooldown = false
		logger.Info("[Key Pool] Key ID %d 冷却结束，已恢复可用 (健康分数: %d)", key.ID, stats.HealthScore)
	}
}

// calculateSmartCooldown 计算智能冷却时间
func (p *KeyPool) calculateSmartCooldown(stats *KeyStats) time.Duration {
	baseCooldown := p.configManager.Get().RateLimitCooldown
	cfg := p.configManager.Get()
	
	// 根据429频率动态调整冷却时间
	if stats.RateLimitCount > 10 {
		// 频繁429，使用更长的冷却时间
		return time.Duration(float64(baseCooldown) * cfg.PenaltyFactor * 2.0)
	} else if stats.RateLimitCount > 5 {
		// 中等频率429
		return time.Duration(float64(baseCooldown) * cfg.PenaltyFactor * 1.5)
	} else if stats.RateLimitCount > 2 {
		// 偶尔429
		return time.Duration(float64(baseCooldown) * cfg.PenaltyFactor)
	}
	
	return time.Duration(baseCooldown)
}

// decreaseHealthScore 降低健康分数
func (p *KeyPool) decreaseHealthScore(stats *KeyStats, amount int) {
	stats.HealthScore -= amount
	if stats.HealthScore < 0 {
		stats.HealthScore = 0
	}
}

// increaseHealthScore 增加健康分数
func (p *KeyPool) increaseHealthScore(stats *KeyStats, amount int) {
	stats.HealthScore += amount
	if stats.HealthScore > 100 {
		stats.HealthScore = 100
	}
}

// scheduleKeyRecovery 安排key恢复
func (p *KeyPool) scheduleKeyRecovery(keyID uint, cooldownDuration time.Duration) {
	time.AfterFunc(cooldownDuration, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		
		if stats, exists := p.keyStats[keyID]; exists {
			stats.IsOnCooldown = false
			
			// 检查key是否仍在allKeys中
			if key, keyExists := p.allKeys[keyID]; keyExists {
				select {
				case p.availableKeys <- key:
					logger.Info("[Key Pool] Key ID %d 冷却结束，已返回可用池 (健康分数: %d)", keyID, stats.HealthScore)
				default:
					logger.Warn("[Key Pool] Key ID %d 冷却结束，但通道已满。", keyID)
				}
			}
		}
	})
}

// GetBannedKeysInfo returns information about keys currently on cooldown.
func (p *KeyPool) GetBannedKeysInfo() ([]BannedKeyInfo, error) {
	var results []BannedKeyInfo

	p.mu.RLock()
	defer p.mu.RUnlock()

	p.cooldownKeys.Range(func(key, value interface{}) bool {
		keyID := key.(uint)
		bannedUntil := value.(time.Time)

		if time.Now().Before(bannedUntil) {
			if apiKey, ok := p.allKeys[keyID]; ok {
				results = append(results, BannedKeyInfo{
					APIKey:      *apiKey,
					BannedUntil: bannedUntil,
				})
			}
		}
		return true // continue iteration
	})

	return results, nil
}

// GetBannedKeyCount returns the number of keys currently on cooldown.
func (p *KeyPool) GetBannedKeyCount() int {
	count := 0
	p.cooldownKeys.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}