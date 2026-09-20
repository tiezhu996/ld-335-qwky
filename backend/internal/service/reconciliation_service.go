package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/blueship581/gbinsureapi/internal/constants"
	"github.com/blueship581/gbinsureapi/internal/model"
	"github.com/blueship581/gbinsureapi/internal/repository"
	"github.com/blueship581/gbinsureapi/internal/util"
	"gorm.io/gorm"
)

// ReconciliationService 日终对账服务（复用 SettlementOrderRepository）。
type ReconciliationService struct {
	orderRepo *repository.SettlementOrderRepository
	recRepo   *repository.DailyReconciliationRepository
	log       *slog.Logger
}

// NewReconciliationService 构造日终对账服务。
func NewReconciliationService(orderRepo *repository.SettlementOrderRepository, recRepo *repository.DailyReconciliationRepository, log *slog.Logger) *ReconciliationService {
	return &ReconciliationService{orderRepo: orderRepo, recRepo: recRepo, log: log}
}

// aggregateDaily 汇总当日有效结算单（仓储已剔除已冲正单）。
// 总笔数、总金额、成功笔数均不含冲正单；该函数是日终对账与冲正刷新的唯一口径。
func aggregateDaily(date string, orders []model.SettlementOrder) *model.DailyReconciliation {
	rec := &model.DailyReconciliation{ReconcileDate: date}
	totalAmount := 0.0
	for _, o := range orders {
		rec.TotalCount++
		totalAmount += o.TotalAmount
		switch o.Status {
		case constants.SettlementSettled:
			rec.SuccessCount++
		case constants.SettlementFailed:
			rec.FailCount++
		case constants.SettlementPendingManual:
			rec.AbnormalOrders++
		}
	}
	rec.TotalAmount = round2(totalAmount)
	return rec
}

// refreshDailyInTx 在既有事务内按仓储口径重算并原子 upsert 某日对账。
// 冲正闭环与 Daily 端点共用本函数，保证对账口径与结算仓储一致。
func (s *ReconciliationService) refreshDailyInTx(ctx context.Context, tx *gorm.DB, clientID uint, date string) (*model.DailyReconciliation, error) {
	orders, err := s.orderRepo.WithTx(tx).TodaySettled(clientID, date)
	if err != nil {
		return nil, util.LogError(s.log, constants.LOG_RECONCILIATION_FAILED, fmt.Errorf("list today orders: %w", err))
	}
	rec := aggregateDaily(date, orders)
	if err := s.recRepo.WithTx(tx).Upsert(rec); err != nil {
		return nil, util.LogError(s.log, constants.LOG_RECONCILIATION_FAILED, fmt.Errorf("upsert reconciliation: %w", err))
	}
	s.log.InfoContext(ctx, constants.LOG_RECONCILIATION_GENERATED, "date", date, "total", rec.TotalCount, "client_id", clientID)
	return rec, nil
}

// RefreshDailyInTx 供冲正闭环在既有事务内刷新当日对账（全量口径 clientID=0）：
// 冲正单提交的同时把当日总笔数、总金额、成功笔数从对账结果中剔除，
// 任一写入失败整个事务回滚，不会留下半更新。
func (s *ReconciliationService) RefreshDailyInTx(ctx context.Context, tx *gorm.DB, date string) (*model.DailyReconciliation, error) {
	return s.refreshDailyInTx(ctx, tx, 0, date)
}

// Daily 生成/更新当日对账（事务内重算 + 原子 upsert，返回最新汇总）。
func (s *ReconciliationService) Daily(ctx context.Context, clientID uint) (*model.DailyReconciliation, error) {
	date := util.TodayDate()
	var rec *model.DailyReconciliation
	err := s.orderRepo.Transaction(func(tx *gorm.DB) error {
		var err error
		rec, err = s.refreshDailyInTx(ctx, tx, clientID, date)
		return err
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// List 分页查询对账记录。
func (s *ReconciliationService) List(ctx context.Context, page, pageSize int) ([]model.DailyReconciliation, int64, error) {
	return s.recRepo.List(page, pageSize)
}
