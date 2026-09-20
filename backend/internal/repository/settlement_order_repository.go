package repository

import (
	"errors"
	"time"

	"github.com/blueship581/gbinsureapi/internal/constants"
	"github.com/blueship581/gbinsureapi/internal/model"
	"github.com/blueship581/gbinsureapi/internal/util"
	"gorm.io/gorm"
)

// SettlementOrderRepository 结算单仓储。
type SettlementOrderRepository struct{ db *gorm.DB }

// NewSettlementOrderRepository 构造结算单仓储。
func NewSettlementOrderRepository(db *gorm.DB) *SettlementOrderRepository {
	return &SettlementOrderRepository{db: db}
}

// WithTx 返回绑定既有事务的仓储副本（冲正闭环多写一致性用）。
func (r *SettlementOrderRepository) WithTx(tx *gorm.DB) *SettlementOrderRepository {
	return &SettlementOrderRepository{db: tx}
}

// Transaction 在同一连接上开启事务，fn 返回错误则整体回滚，不留半更新。
func (r *SettlementOrderRepository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

// Create 创建结算单。
func (r *SettlementOrderRepository) Create(order *model.SettlementOrder) error {
	return r.db.Create(order).Error
}

// FindByNo 按结算单号查询。
func (r *SettlementOrderRepository) FindByNo(no string) (*model.SettlementOrder, error) {
	var order model.SettlementOrder
	if err := r.db.Where("settlement_no = ?", no).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, util.ErrNotFound
		}
		return nil, err
	}
	return &order, nil
}

// ExistsByNo 结算单号是否存在。
func (r *SettlementOrderRepository) ExistsByNo(no string) (bool, error) {
	var count int64
	err := r.db.Model(&model.SettlementOrder{}).Where("settlement_no = ?", no).Count(&count).Error
	return count > 0, err
}

// MarkReversedToday 原子条件更新：仅当日已结算（settled）单可冲正。
// 单条 UPDATE 完成 settled -> reversed 状态迁移，并发到达时只有一行受影响，
// 未命中（0 行）时不产生任何写入，失败不会留下半更新。
// start/end 为 util.TodayBounds 给出的当日 [起, 止) 区间（UTC 瞬时）。
func (r *SettlementOrderRepository) MarkReversedToday(tx *gorm.DB, no string, start, end, reversedAt time.Time) (int64, error) {
	res := tx.Model(&model.SettlementOrder{}).
		Where("settlement_no = ?", no).
		Where("status = ?", constants.SettlementSettled).
		Where("settled_at >= ? AND settled_at < ?", start, end).
		Updates(map[string]any{
			"status":      constants.SettlementReversed,
			"reversed_at": reversedAt,
		})
	return res.RowsAffected, res.Error
}

// List 分页查询（历史结算查询：已冲正单仍保留在结果中）。
func (r *SettlementOrderRepository) List(clientID uint, status string, page, pageSize int) ([]model.SettlementOrder, int64, error) {
	q := r.db.Model(&model.SettlementOrder{})
	if clientID > 0 {
		q = q.Where("client_id = ?", clientID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var orders []model.SettlementOrder
	err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&orders).Error
	return orders, total, err
}

// TodaySettled 当日有效结算单（日终对账口径）。
// 与冲正闭环共用同一日界（util.DayBounds），且剔除已冲正（reversed）单：
// 冲正单不计入日终总笔数、总金额与成功笔数；历史查询请使用 List。
func (r *SettlementOrderRepository) TodaySettled(clientID uint, date string) ([]model.SettlementOrder, error) {
	start, end := util.DayBounds(date)
	var orders []model.SettlementOrder
	q := r.db.Where("settled_at >= ? AND settled_at < ?", start, end).
		Where("status <> ?", constants.SettlementReversed)
	if clientID > 0 {
		q = q.Where("client_id = ?", clientID)
	}
	err := q.Find(&orders).Error
	return orders, err
}

// Count 统计。
func (r *SettlementOrderRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&model.SettlementOrder{}).Count(&count).Error
	return count, err
}
