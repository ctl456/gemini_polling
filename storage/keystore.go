package storage

import (
	"errors"
	"fmt"
	"gemini_polling/model" // <-- 注意导入路径的变化
	"log"

	"gorm.io/gorm"
)

type KeyStore struct {
	db *gorm.DB
}

func NewKeyStore(db *gorm.DB) *KeyStore {
	return &KeyStore{db: db}
}

func (s *KeyStore) Add(apiKeyVal string) (*model.APIKey, error) {
	key := &model.APIKey{Key: apiKeyVal, Enabled: true}
	if result := s.db.Create(key); result.Error != nil {
		return nil, result.Error
	}
	return key, nil
}

// FindByKey 仅用于用户鉴权，检查用户提供的key是否存在且有效
func (s *KeyStore) FindByKey(apiKeyVal string) (*model.APIKey, error) {
	var key model.APIKey
	// 同样，在这里也需要将 key 用反引号包围
	result := s.db.Where("`key` = ? AND enabled = ?", apiKeyVal, true).First(&key)
	if result.Error != nil {
		return nil, result.Error
	}
	return &key, nil
}

// GetNextActiveKey 高效地从数据库中随机获取一个有效的Key用于轮询
func (s *KeyStore) GetNextActiveKey() (*model.APIKey, error) {
	var key model.APIKey
	var randomFunc string
	// 从 GORM 的 Dialector 获取数据库类型名称
	driverName := s.db.Dialector.Name() // "mysql", "sqlite", "postgres", etc.
	switch driverName {
	case "mysql":
		randomFunc = "RAND()"
	case "sqlite": // 注意: gorm-sqlite驱动返回 "sqlite"
		randomFunc = "RANDOM()"
	default:
		log.Printf("警告: 不支持的 Dialector '%s' 用于随机排序，将默认使用 RANDOM()", driverName)
		randomFunc = "RANDOM()"
	}
	err := s.db.Where("enabled = ?", true).Order(randomFunc).First(&key).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("没有可用的 active API keys")
		}
		return nil, err
	}
	return &key, nil
}

// ListKeys 支持分页和状态过滤，并返回总数
func (s *KeyStore) ListKeys(page, pageSize int, status string) ([]model.APIKey, int64, error) {
	var keys []model.APIKey
	var total int64
	query := s.db.Model(&model.APIKey{})
	countQuery := s.db.Model(&model.APIKey{})
	// 应用状态过滤器
	if status == "enabled" {
		query = query.Where("enabled = ?", true)
		countQuery = countQuery.Where("enabled = ?", true)
	} else if status == "disabled" {
		query = query.Where("enabled = ?", false)
		countQuery = countQuery.Where("enabled = ?", false)
	}
	// 如果 status 为空或其他值，则不进行状态过滤，返回所有
	// 首先获取总记录数（在应用分页之前）
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	// 应用分页和排序
	err := query.Offset((page - 1) * pageSize).Limit(pageSize).Order("id DESC").Find(&keys).Error
	if err != nil {
		return nil, 0, err
	}
	return keys, total, nil
}

// FindByID 根据ID查找Key
func (s *KeyStore) FindByID(id uint) (*model.APIKey, error) {
	var key model.APIKey
	if err := s.db.First(&key, id).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

func (s *KeyStore) SetEnabled(id uint, enabled bool) error {
	result := s.db.Model(&model.APIKey{}).Where("id = ?", id).Update("enabled", enabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Disable 是一个辅助函数，在API调用失败时可被调用
func (s *KeyStore) Disable(id uint, reason string) {
	log.Printf("正在禁用 Key ID %d，原因: %s", id, reason)
	if err := s.SetEnabled(id, false); err != nil {
		log.Printf("自动禁用 Key ID %d 失败: %v", id, err)
	}
}

func (s *KeyStore) Delete(id uint) error {
	result := s.db.Delete(&model.APIKey{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *KeyStore) BatchDelete(ids []uint) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := s.db.Delete(&model.APIKey{}, "id IN ?", ids)
	return result.RowsAffected, result.Error
}

// +新增: 这个函数专门为 KeyScanner 服务
func (s *KeyStore) GetAllEnabledKeys() ([]model.APIKey, error) {
	var keys []model.APIKey
	if err := s.db.Where("enabled = ?", true).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

func (s *KeyStore) AddMultiple(keys []string) (addedCount int, skippedCount int, err error) {
	if len(keys) == 0 {
		return 0, 0, nil
	}
	// 1. 查询这批 keys 中哪些已经存在于数据库
	var existingKeys []string
	if err := s.db.Model(&model.APIKey{}).Select("`key`").Where("`key` IN ?", keys).Find(&existingKeys).Error; err != nil {
		return 0, 0, fmt.Errorf("查询已存在的key时出错: %w", err)
	}
	// 2. 使用 map 快速查找已存在的 key
	existingMap := make(map[string]bool)
	for _, k := range existingKeys {
		existingMap[k] = true
	}
	skippedCount = len(existingMap)
	// 3. 准备要插入的新 key 列表
	var keysToInsert []*model.APIKey
	for _, k := range keys {
		if !existingMap[k] {
			keysToInsert = append(keysToInsert, &model.APIKey{Key: k, Enabled: true})
		}
	}
	// 4. 如果有新 key 需要插入，则执行批量插入
	if len(keysToInsert) > 0 {
		if result := s.db.Create(&keysToInsert); result.Error != nil {
			return 0, skippedCount, fmt.Errorf("批量插入新key时出错: %w", result.Error)
		}
		addedCount = int(len(keysToInsert))
	}
	return addedCount, skippedCount, nil
}
