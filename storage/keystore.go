package storage

import (
	"errors"
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
	// 注意，这里我们假设用户鉴权的key和后台轮询的key是同一批
	// 这在很多代理中是常见做法。如果用户key是独立的，需要另一张表。
	result := s.db.Where("key = ? AND enabled = ?", apiKeyVal, true).First(&key)
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
