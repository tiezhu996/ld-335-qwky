package service

import (
	"context"
	"testing"
	"time"

	"github.com/blueship581/gbinsureapi/internal/constants"
	"github.com/blueship581/gbinsureapi/internal/model"
	"github.com/blueship581/gbinsureapi/internal/repository"
	"github.com/blueship581/gbinsureapi/internal/util"
	"gorm.io/gorm"
)

// seedOrderWithStatus 落库一笔指定状态与结算时间的结算单。
func seedOrderWithStatus(t *testing.T, db *gorm.DB, clientID, personID uint, status string, settledAt time.Time, amount float64) model.SettlementOrder {
	t.Helper()
	order := model.SettlementOrder{
		SettlementNo: util.SettlementNo(time.Now().UnixNano()), BatchID: 1, InsuredPersonID: personID,
		PresettlementID: 1, ClientID: clientID, Status: status,
		TotalAmount: amount, InsurancePayAmount: amount * 0.7, SettledAt: &settledAt,
	}
	if status == constants.SettlementReversed {
		order.ReversedAt = &settledAt
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return order
}

// 对账闭环：冲正单从总笔数/总金额/成功笔数剔除，单列冲正栏；历史查询仍保留。
func TestReconciliationService_DailyExcludesReversed(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	todayStart, _, err := util.DayRange(util.TodayDate())
	if err != nil {
		t.Fatalf("DayRange() error = %v", err)
	}
	at := func(h int) time.Time { return todayStart.Add(time.Duration(h) * time.Hour) }
	// 当日：2 笔已结算 + 1 笔失败 + 1 笔已冲正；另 1 笔昨日已结算（不计入）
	seedOrderWithStatus(t, db, clientID, personID, constants.SettlementSettled, at(9), 1000)
	seedOrderWithStatus(t, db, clientID, personID, constants.SettlementSettled, at(10), 2000)
	seedOrderWithStatus(t, db, clientID, personID, constants.SettlementFailed, at(11), 300)
	reversed := seedOrderWithStatus(t, db, clientID, personID, constants.SettlementReversed, at(12), 5000)
	seedOrderWithStatus(t, db, clientID, personID, constants.SettlementSettled, at(-2), 999)

	svc := NewReconciliationService(
		repository.NewSettlementOrderRepository(db),
		repository.NewDailyReconciliationRepository(db),
		testLogger(),
	)
	rec, err := svc.Daily(ctx, clientID)
	if err != nil {
		t.Fatalf("Daily() error = %v", err)
	}
	// 冲正单剔除：总笔数 3（不含冲正 1 笔与昨日 1 笔），总金额 3300，成功 2 笔
	if rec.TotalCount != 3 {
		t.Fatalf("TotalCount = %d, want 3", rec.TotalCount)
	}
	if rec.TotalAmount != 3300 {
		t.Fatalf("TotalAmount = %v, want 3300", rec.TotalAmount)
	}
	if rec.SuccessCount != 2 {
		t.Fatalf("SuccessCount = %d, want 2", rec.SuccessCount)
	}
	if rec.FailCount != 1 {
		t.Fatalf("FailCount = %d, want 1", rec.FailCount)
	}
	if rec.ReversedCount != 1 || rec.ReversedAmount != 5000 {
		t.Fatalf("reversed = (%d, %v), want (1, 5000)", rec.ReversedCount, rec.ReversedAmount)
	}
	// 历史结算查询仍保留已冲正单
	orders, total, err := repository.NewSettlementOrderRepository(db).List(clientID, constants.SettlementReversed, 1, 10)
	if err != nil || total != 1 || len(orders) != 1 || orders[0].SettlementNo != reversed.SettlementNo {
		t.Fatalf("history query must retain reversed order: total=%d err=%v", total, err)
	}
}

// 对账闭环：重复执行幂等 upsert（同一日期仅一行），且与冲正后的结算仓储口径一致。
func TestReconciliationService_DailyUpsertConsistentWithReversal(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	todayStart, _, err := util.DayRange(util.TodayDate())
	if err != nil {
		t.Fatalf("DayRange() error = %v", err)
	}
	at := func(h int) time.Time { return todayStart.Add(time.Duration(h) * time.Hour) }
	seedOrderWithStatus(t, db, clientID, personID, constants.SettlementSettled, at(9), 1000)
	victim := seedOrderWithStatus(t, db, clientID, personID, constants.SettlementSettled, at(10), 4000)

	orderRepo := repository.NewSettlementOrderRepository(db)
	recRepo := repository.NewDailyReconciliationRepository(db)
	reconSvc := NewReconciliationService(orderRepo, recRepo, testLogger())

	first, err := reconSvc.Daily(ctx, clientID)
	if err != nil {
		t.Fatalf("Daily() first error = %v", err)
	}
	if first.TotalCount != 2 || first.TotalAmount != 5000 || first.SuccessCount != 2 {
		t.Fatalf("first daily = (%d, %v, %d), want (2, 5000, 2)", first.TotalCount, first.TotalAmount, first.SuccessCount)
	}

	// 冲正其中一笔后再跑对账：口径必须与结算仓储一致（剔除该单）
	settleSvc := newSettlementSvc(db)
	if _, _, err := settleSvc.ReverseSettlement(ctx, victim.SettlementNo); err != nil {
		t.Fatalf("ReverseSettlement() error = %v", err)
	}
	second, err := reconSvc.Daily(ctx, clientID)
	if err != nil {
		t.Fatalf("Daily() second error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("upsert should reuse row id %d, got %d", first.ID, second.ID)
	}
	if second.TotalCount != 1 || second.TotalAmount != 1000 || second.SuccessCount != 1 {
		t.Fatalf("second daily = (%d, %v, %d), want (1, 1000, 1)", second.TotalCount, second.TotalAmount, second.SuccessCount)
	}
	if second.ReversedCount != 1 || second.ReversedAmount != 4000 {
		t.Fatalf("reversed = (%d, %v), want (1, 4000)", second.ReversedCount, second.ReversedAmount)
	}
	// 同一日期仅一行，不产生重复对账记录
	var rowCount int64
	if err := db.Model(&model.DailyReconciliation{}).Where("reconcile_date = ?", util.TodayDate()).Count(&rowCount).Error; err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("reconciliation rows = %d, want 1", rowCount)
	}
	// 仓储口径复核：SettledBetween 仍含冲正单（共 2 行），剔除逻辑只在服务层
	all, err := orderRepo.SettledBetween(clientID, todayStart, todayStart.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("SettledBetween() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("SettledBetween len = %d, want 2", len(all))
	}
}
