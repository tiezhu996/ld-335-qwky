package repository

import (
	"errors"

	"github.com/blueship581/gbinsureapi/internal/model"
	"github.com/blueship581/gbinsureapi/internal/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DailyReconciliationRepository 日终对账仓储。
type DailyReconciliationRepository struct{ db *gorm.DB }

// NewDailyReconciliationRepository 构造日终对账仓储。
func NewDailyReconciliationRepository(db *gorm.DB) *DailyReconciliationRepository {
	return &DailyReconciliationRepository{db: db}
}

// WithTx 返回绑定既有事务的仓储副本（冲正闭环内刷新对账用）。
func (r *DailyReconciliationRepository) WithTx(tx *gorm.DB) *DailyReconciliationRepository {
	return &DailyReconciliationRepository{db: tx}
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

// Upsert 按 reconcile_date 原子写入当日对账（存在则整行覆盖汇总列）。
// 单条 INSERT ... ON CONFLICT 语句完成，并发调用与冲正刷新均不会留下半更新。
func (r *DailyReconciliationRepository) Upsert(rec *model.DailyReconciliation) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "reconcile_date"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"total_count", "total_amount", "success_count", "fail_count", "abnormal_orders",
		}),
	}).Create(rec).Error
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
