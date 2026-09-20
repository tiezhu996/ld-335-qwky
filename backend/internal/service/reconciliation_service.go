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

// ReconciliationService 日终对账服务（复用 SettlementOrderRepository，取数口径与结算仓储一致）。
type ReconciliationService struct {
	orderRepo *repository.SettlementOrderRepository
	recRepo   *repository.DailyReconciliationRepository
	log       *slog.Logger
}

// NewReconciliationService 构造日终对账服务。
func NewReconciliationService(orderRepo *repository.SettlementOrderRepository, recRepo *repository.DailyReconciliationRepository, log *slog.Logger) *ReconciliationService {
	return &ReconciliationService{orderRepo: orderRepo, recRepo: recRepo, log: log}
}

// Daily 生成/更新当日对账（幂等 upsert，返回最新汇总）。
// 口径闭环：
//  1. 取数与结算仓储共用 SettledBetween（当日结算时间窗），不再各自拼过滤条件；
//  2. 冲正单从 total_count/total_amount/success_count 中剔除，单独计入 reversed_*；
//  3. 汇总读取与 upsert 在同一事务内完成，任何失败整体回滚，不留半更新。
func (s *ReconciliationService) Daily(ctx context.Context, clientID uint) (*model.DailyReconciliation, error) {
	date := util.TodayDate()
	start, end, err := util.DayRange(date)
	if err != nil {
		return nil, util.LogError(s.log, constants.LOG_RECONCILIATION_FAILED, fmt.Errorf("parse day range: %w", err))
	}
	var rec *model.DailyReconciliation
	err = s.recRepo.TransactionWith(func(tx *gorm.DB, recTx *repository.DailyReconciliationRepository) error {
		orders, err := s.orderRepo.WithTx(tx).SettledBetween(clientID, start, end)
		if err != nil {
			return util.LogError(s.log, constants.LOG_RECONCILIATION_FAILED, fmt.Errorf("list today orders: %w", err))
		}
		summary := summarizeDaily(orders)
		rec = &model.DailyReconciliation{
			ReconcileDate: date, TotalCount: summary.totalCount, TotalAmount: round2(summary.totalAmount),
			SuccessCount: summary.successCount, FailCount: summary.failCount, AbnormalOrders: summary.abnormalCount,
			ReversedCount: summary.reversedCount, ReversedAmount: round2(summary.reversedAmount),
		}
		if err := recTx.Upsert(rec); err != nil {
			return util.LogError(s.log, constants.LOG_RECONCILIATION_FAILED, fmt.Errorf("upsert reconciliation: %w", err))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if rec.ReversedCount > 0 {
		s.log.InfoContext(ctx, constants.LOG_RECONCILIATION_REVERSED_EXCLUDED,
			"date", date, "reversed_count", rec.ReversedCount, "reversed_amount", rec.ReversedAmount)
	}
	s.log.InfoContext(ctx, constants.LOG_RECONCILIATION_GENERATED,
		"date", date, "total", rec.TotalCount, "success", rec.SuccessCount, "reversed", rec.ReversedCount)
	return rec, nil
}

// dailySummary 当日对账汇总中间态（冲正剔除规则的唯一实现处）。
type dailySummary struct {
	totalCount     int64
	totalAmount    float64
	successCount   int64
	failCount      int64
	abnormalCount  int64
	reversedCount  int64
	reversedAmount float64
}

// summarizeDaily 汇总当日结算单：已冲正单全部剔除出总笔数/总金额/成功笔数，仅计入冲正栏。
func summarizeDaily(orders []model.SettlementOrder) dailySummary {
	var sum dailySummary
	for _, o := range orders {
		if o.Status == constants.SettlementReversed {
			sum.reversedCount++
			sum.reversedAmount += o.TotalAmount
			continue
		}
		sum.totalCount++
		sum.totalAmount += o.TotalAmount
		switch o.Status {
		case constants.SettlementSettled:
			sum.successCount++
		case constants.SettlementFailed:
			sum.failCount++
		case constants.SettlementPendingManual:
			sum.abnormalCount++
		}
	}
	return sum
}

// List 分页查询对账记录。
func (s *ReconciliationService) List(ctx context.Context, page, pageSize int) ([]model.DailyReconciliation, int64, error) {
	return s.recRepo.List(page, pageSize)
}
