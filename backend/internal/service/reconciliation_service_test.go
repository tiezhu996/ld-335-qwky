package service

import (
	"context"
	"testing"
	"time"

	"github.com/blueship581/gbinsureapi/internal/constants"
	"github.com/blueship581/gbinsureapi/internal/model"
	"github.com/blueship581/gbinsureapi/internal/repository"
	"github.com/blueship581/gbinsureapi/internal/util"
)

// TestReconciliationService_DailyAggregates 对账口径：剔除冲正单，统计成功/失败/异常。
func TestReconciliationService_DailyAggregates(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	svc, recon := newSettlementSvc(t, db)
	now := time.Now()
	yesterday := now.Add(-25 * time.Hour)
	seed := func(no, status string, at time.Time, amount float64) {
		t.Helper()
		o := model.SettlementOrder{
			SettlementNo: no, BatchID: 1, InsuredPersonID: personID, PresettlementID: 1,
			ClientID: clientID, Status: status, TotalAmount: amount, SettledAt: &at,
		}
		if err := db.Create(&o).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed("GB-A-SETTLED", constants.SettlementSettled, now, 1000)
	seed("GB-A-FAILED", constants.SettlementFailed, now, 200)
	seed("GB-A-MANUAL", constants.SettlementPendingManual, now, 300)
	seed("GB-A-REVERSED", constants.SettlementReversed, now, 700) // 已冲正：必须剔除
	seed("GB-A-OLD", constants.SettlementSettled, yesterday, 900) // 非当日：必须剔除

	rec, err := recon.Daily(ctx, 0)
	if err != nil {
		t.Fatalf("Daily() error = %v", err)
	}
	if rec.TotalCount != 3 || rec.TotalAmount != 1500 || rec.SuccessCount != 1 || rec.FailCount != 1 || rec.AbnormalOrders != 1 {
		t.Fatalf("rec = %+v, want 3/1500/1/1/1", rec)
	}
	// 幂等：重复生成结果一致
	again, err := recon.Daily(ctx, 0)
	if err != nil || again.TotalCount != 3 || again.TotalAmount != 1500 {
		t.Fatalf("Daily() not idempotent: %+v, err = %v", again, err)
	}
	// 仓储口径与服务口径一致（同一查询路径）
	orders, err := repository.NewSettlementOrderRepository(db).TodaySettled(0, util.TodayDate())
	if err != nil || len(orders) != 3 {
		t.Fatalf("TodaySettled = %d, err = %v, want 3", len(orders), err)
	}
	_ = svc
}

// TestReconciliationService_RefreshMatchesDaily 冲正闭环刷新与 Daily 端点同口径。
func TestReconciliationService_RefreshMatchesDaily(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	svc, recon := newSettlementSvc(t, db)
	seedSettledOrder(t, db, clientID, personID, "GB-B-1", time.Now(), 500)
	victim := seedSettledOrder(t, db, clientID, personID, "GB-B-2", time.Now(), 300)

	if _, _, err := svc.ReverseSettlement(ctx, victim.SettlementNo); err != nil {
		t.Fatalf("ReverseSettlement() error = %v", err)
	}
	// 冲正事务内刷新的存储记录
	stored, err := repository.NewDailyReconciliationRepository(db).FindByDate(util.TodayDate())
	if err != nil {
		t.Fatalf("FindByDate() error = %v", err)
	}
	// Daily 端点重新计算
	fresh, err := recon.Daily(ctx, 0)
	if err != nil {
		t.Fatalf("Daily() error = %v", err)
	}
	if stored.TotalCount != fresh.TotalCount || stored.TotalAmount != fresh.TotalAmount || stored.SuccessCount != fresh.SuccessCount {
		t.Fatalf("stored = %+v, fresh = %+v, caliber mismatch", stored, fresh)
	}
	if fresh.TotalCount != 1 || fresh.TotalAmount != 500 || fresh.SuccessCount != 1 {
		t.Fatalf("fresh = %+v, want 1/500/1", fresh)
	}
}

// TestSettlementService_ReverseRollbackOnReconciliationFailure 对账刷新失败时冲正整体回滚，不留半更新。
func TestSettlementService_ReverseRollbackOnReconciliationFailure(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	svc, _ := newSettlementSvc(t, db)
	order := seedSettledOrder(t, db, clientID, personID, "GB-C-1", time.Now(), 800)

	// 破坏对账表，使事务内的对账刷新必然失败
	if err := db.Migrator().DropTable(&model.DailyReconciliation{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ReverseSettlement(ctx, order.SettlementNo); err == nil {
		t.Fatal("expected error when reconciliation refresh fails")
	}
	// 状态迁移必须一并回滚：订单仍为已结算，未留下 reversed_at
	got, err := svc.GetOrder(ctx, order.SettlementNo)
	if err != nil {
		t.Fatalf("GetOrder() error = %v", err)
	}
	if got.Status != constants.SettlementSettled || got.ReversedAt != nil {
		t.Fatalf("half update detected: status=%s reversed_at=%v", got.Status, got.ReversedAt)
	}
}
