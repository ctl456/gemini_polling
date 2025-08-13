// service/key_pool_service.go
package service

import (
	"errors"
	"gemini_polling/config"
	"gemini_polling/model"
	"gemini_polling/storage"
	"log"
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
}

// NewKeyPool creates a new KeyPool service.
func NewKeyPool(keyStore *storage.KeyStore, configManager *config.Manager) *KeyPool {
	return &KeyPool{
		keyStore:      keyStore,
		configManager: configManager,
		allKeys:       make(map[uint]*model.APIKey),
	}
}

// Start initializes the pool and begins periodic refresh operations.
func (p *KeyPool) Start(refreshInterval time.Duration) {
	log.Println("启动内存 Key 池服务...")
	p.initialLoad()

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			log.Println("[Key Pool] 定时刷新 Key 列表...")
			p.refresh()
		}
	}()
}

// initialLoad performs the first load of keys from the database.
func (p *KeyPool) initialLoad() {
	keys, err := p.keyStore.GetAllEnabledKeys()
	if err != nil {
		log.Printf("[错误] Key 池初始化加载失败: %v", err)
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

	log.Printf("Key 池初始化成功，加载了 %d 个可用的 Key。", len(keys))
}

// refresh reloads keys from the database and updates the pool.
func (p *KeyPool) refresh() {
	dbKeys, err := p.keyStore.GetAllEnabledKeys()
	if err != nil {
		log.Printf("[错误] Key 池刷新失败: %v", err)
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

	log.Printf("[Key Pool] 刷新完成。数据库中共有 %d 个启用 Key，当前可用 %d 个。", len(p.allKeys), refreshedCount)
}

// GetKey retrieves an available key from the pool.
// It blocks until a key is available.
func (p *KeyPool) GetKey() (*model.APIKey, error) {
	select {
	case key, ok := <-p.availableKeys:
		if !ok {
			return nil, errors.New("key channel is closed")
		}
		return key, nil
	case <-time.After(30 * time.Second): // Add a timeout to prevent indefinite blocking
		return nil, ErrNoAvailableKeys
	}
}

// ReturnKey returns a key to the pool, optionally putting it on cooldown.
func (p *KeyPool) ReturnKey(key *model.APIKey, isRateLimited bool) {
	if key == nil {
		return
	}

	if isRateLimited {
		cooldownDuration := p.configManager.Get().RateLimitCooldown
		cooldownUntil := time.Now().Add(cooldownDuration)
		p.cooldownKeys.Store(key.ID, cooldownUntil)

		log.Printf("[Key Pool] Key ID %d 因速率限制进入冷却，时长: %v", key.ID, cooldownDuration)

		// After the cooldown, return the key to the available pool.
		time.AfterFunc(cooldownDuration, func() {
			p.cooldownKeys.Delete(key.ID)
			// Before returning, double-check if the key is still considered enabled in the main map.
			p.mu.RLock()
			_, stillExists := p.allKeys[key.ID]
			p.mu.RUnlock()

			if stillExists {
				p.availableKeys <- key
				log.Printf("[Key Pool] Key ID %d 冷却结束，已返回可用池。", key.ID)
			} else {
				log.Printf("[Key Pool] Key ID %d 冷却结束，但它已不再是启用状态，不返回可用池。", key.ID)
			}
		})
	} else {
		// Return directly to the pool if not rate-limited.
		p.availableKeys <- key
	}
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
