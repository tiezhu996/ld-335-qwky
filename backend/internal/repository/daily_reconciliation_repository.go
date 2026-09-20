package repository

import (
	"errors"

	"github.com/blueship581/gbinsureapi/internal/model"
	"github.com/blueship581/gbinsureapi/internal/util"
	"gorm.io/gorm"
)

// DailyReconciliationRepository 日终对账仓储。
type DailyReconciliationRepository struct{ db *gorm.DB }

// NewDailyReconciliationRepository 构造日终对账仓储。
func NewDailyReconciliationRepository(db *gorm.DB) *DailyReconciliationRepository {
	return &DailyReconciliationRepository{db: db}
}

// WithTx 返回绑定事务连接的仓储副本（与结算仓储共用事务边界）。
func (r *DailyReconciliationRepository) WithTx(tx *gorm.DB) *DailyReconciliationRepository {
	return &DailyReconciliationRepository{db: tx}
}

// TransactionWith 在事务中执行 fn：fn 拿到底层事务句柄与对账仓储事务副本，
// 调用方可用同一句柄绑定结算仓储，保证汇总读取与 upsert 同生共死、失败整体回滚。
func (r *DailyReconciliationRepository) TransactionWith(fn func(tx *gorm.DB, txRepo *DailyReconciliationRepository) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return fn(tx, r.WithTx(tx))
	})
}

// Create 创建对账记录。
func (r *DailyReconciliationRepository) Create(rec *model.DailyReconciliation) error {
	return r.db.Create(rec).Error
}

// FindByDate 按日期查询。
func (r *DailyReconciliationRepository) FindByDate(date string) (*model.DailyReconciliation, error) {
	var rec model.DailyReconciliation
	if err := r.db.Where("reconcile_date = ?", date).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, util.ErrNotFound
		}
		return nil, err
	}
	return &rec, nil
}

// Upsert 按 reconcile_date 幂等写入：存在则整行覆盖汇总值，不存在则新建。
// 调用方须保证在事务内使用，避免并发下查-写分离产生半更新。
func (r *DailyReconciliationRepository) Upsert(rec *model.DailyReconciliation) error {
	existing, err := r.FindByDate(rec.ReconcileDate)
	if err != nil {
		if errors.Is(err, util.ErrNotFound) {
			return r.db.Create(rec).Error
		}
		return err
	}
	rec.ID = existing.ID
	rec.CreatedAt = existing.CreatedAt
	return r.db.Save(rec).Error
}

// List 分页查询。
func (r *DailyReconciliationRepository) List(page, pageSize int) ([]model.DailyReconciliation, int64, error) {
	var total int64
	if err := r.db.Model(&model.DailyReconciliation{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.DailyReconciliation
	err := r.db.Order("reconcile_date desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error
	return items, total, err
}

// Update 更新对账记录。
func (r *DailyReconciliationRepository) Update(rec *model.DailyReconciliation) error {
	return r.db.Save(rec).Error
}
