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

// WithTx 返回绑定事务连接的仓储副本（冲正/对账共用，保证同一事务边界）。
func (r *SettlementOrderRepository) WithTx(tx *gorm.DB) *SettlementOrderRepository {
	return &SettlementOrderRepository{db: tx}
}

// Transaction 在事务中执行 fn：任一失败整体回滚，不留半更新状态。
func (r *SettlementOrderRepository) Transaction(fn func(txRepo *SettlementOrderRepository) error) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return fn(r.WithTx(tx))
	})
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

// Update 更新结算单。
func (r *SettlementOrderRepository) Update(order *model.SettlementOrder) error { return r.db.Save(order).Error }

// MarkReversed 原子状态迁移：仅当当前状态为 settled 时置为 reversed。
// 并发冲正下数据库行锁保证只有一个请求受影响行数为 1，其余为 0（不得重复迁移）。
func (r *SettlementOrderRepository) MarkReversed(no string, reversedAt time.Time) (int64, error) {
	res := r.db.Model(&model.SettlementOrder{}).
		Where("settlement_no = ? AND status = ?", no, constants.SettlementSettled).
		Updates(map[string]any{
			"status":      constants.SettlementReversed,
			"reversed_at": reversedAt,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// List 分页查询（历史结算查询保留全部状态，含已冲正单；count 与 find 过滤条件必须一致）。
func (r *SettlementOrderRepository) List(clientID uint, status string, page, pageSize int) ([]model.SettlementOrder, int64, error) {
	applyFilters := func(q *gorm.DB) *gorm.DB {
		if clientID > 0 {
			q = q.Where("client_id = ?", clientID)
		}
		if status != "" {
			q = q.Where("status = ?", status)
		}
		return q
	}
	var total int64
	if err := applyFilters(r.db.Model(&model.SettlementOrder{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var orders []model.SettlementOrder
	err := applyFilters(r.db.Model(&model.SettlementOrder{})).
		Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&orders).Error
	return orders, total, err
}

// SettledBetween 结算时间落在 [start, end) 内的全部结算单（含已冲正）。
// 这是日终对账与结算仓储共用的统一取数口径：冲正单由服务层按 status 剔除，仓储不再各自过滤。
func (r *SettlementOrderRepository) SettledBetween(clientID uint, start, end time.Time) ([]model.SettlementOrder, error) {
	var orders []model.SettlementOrder
	q := r.db.Where("settled_at >= ? AND settled_at < ?", start, end)
	if clientID > 0 {
		q = q.Where("client_id = ?", clientID)
	}
	err := q.Order("id asc").Find(&orders).Error
	return orders, err
}

// Count 统计。
func (r *SettlementOrderRepository) Count() (int64, error) {
	var count int64
	err := r.db.Model(&model.SettlementOrder{}).Count(&count).Error
	return count, err
}
