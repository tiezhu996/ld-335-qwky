package service

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/blueship581/gbinsureapi/internal/constants"
	"github.com/blueship581/gbinsureapi/internal/model"
	"github.com/blueship581/gbinsureapi/internal/repository"
	"github.com/blueship581/gbinsureapi/internal/util"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 单连接：:memory: 库跨连接不共享，且让并发事务在数据库层串行。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&model.ApiClient{}, &model.InsuredPerson{}, &model.UploadBatch{}, &model.FeeItem{},
		&model.Presettlement{}, &model.SettlementOrder{}, &model.DailyReconciliation{}, &model.AuditLog{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
}

func seedData(t *testing.T, db *gorm.DB) (uint, uint) {
	t.Helper()
	client := model.ApiClient{Name: "测试HIS", ClientType: constants.ClientTypeHIS, APIKeyHash: "h", Role: "settlement", Status: constants.ClientActive, RateLimitQPS: 10}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	person := model.InsuredPerson{IDCardNo: "110101199001011234", MedicalCardNo: "M110101199001011234", Name: "张三", InsuranceType: constants.InsuranceTypeEmployee, InsuranceStatus: constants.InsuranceStatusActive, InsurancePlace: "北京市", PersonalBalance: 3000}
	if err := db.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	return client.ID, person.ID
}

// newSettlementSvc 装配结算 + 对账服务（冲正闭环依赖）。
func newSettlementSvc(t *testing.T, db *gorm.DB) (*SettlementService, *ReconciliationService) {
	t.Helper()
	log := testLogger()
	insurance := NewInsuranceService(repository.NewInsuredPersonRepository(db), log)
	orderRepo := repository.NewSettlementOrderRepository(db)
	recon := NewReconciliationService(orderRepo, repository.NewDailyReconciliationRepository(db), log)
	svc := NewSettlementService(
		repository.NewPresettlementRepository(db), orderRepo,
		repository.NewFeeItemRepository(db), repository.NewUploadBatchRepository(db),
		insurance, recon, util.NewSettlementCalculator(), log,
	)
	return svc, recon
}

// seedSettledOrder 直接落库一条指定结算时间的已结算单。
func seedSettledOrder(t *testing.T, db *gorm.DB, clientID, personID uint, no string, settledAt time.Time, amount float64) model.SettlementOrder {
	t.Helper()
	order := model.SettlementOrder{
		SettlementNo: no, BatchID: 1, InsuredPersonID: personID, PresettlementID: 1,
		ClientID: clientID, Status: constants.SettlementSettled,
		TotalAmount: amount, InsurancePayAmount: amount * 0.7, SettledAt: &settledAt,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	return order
}

func TestSettlementService_SubmitAndReverse(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	feeRepo := repository.NewFeeItemRepository(db)
	batch := model.UploadBatch{BatchNo: util.BatchNo(1), ClientID: clientID, InsuredPersonID: personID, UploadStatus: constants.UploadValidated, TotalAmount: 1000, ItemCount: 2}
	if err := db.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	if err := feeRepo.CreateBatch([]model.FeeItem{
		{BatchID: batch.ID, ItemCode: "DRUG01", ItemName: "阿莫西林", ItemType: constants.FeeItemDrug, Amount: 600, MedicalCategory: constants.MedicalCategoryClassA},
		{BatchID: batch.ID, ItemCode: "EXAM01", ItemName: "血常规", ItemType: constants.FeeItemExam, Amount: 400, MedicalCategory: constants.MedicalCategoryClassA},
	}); err != nil {
		t.Fatal(err)
	}
	svc, _ := newSettlementSvc(t, db)
	preset, err := svc.CalculatePresettlement(ctx, batch.ID)
	if err != nil {
		t.Fatalf("CalculatePresettlement() error = %v", err)
	}
	if preset.TotalAmount != 1000 {
		t.Fatalf("TotalAmount = %v, want 1000", preset.TotalAmount)
	}
	order, err := svc.SubmitSettlement(ctx, clientID, preset.ID)
	if err != nil {
		t.Fatalf("SubmitSettlement() error = %v", err)
	}
	if order.SettlementNo == "" || order.Status != constants.SettlementSettled {
		t.Fatalf("order invalid: %+v", order)
	}
	// 重复结算同一预结算应再次成功生成新单（幂等性由单号唯一保证）
	order2, err := svc.SubmitSettlement(ctx, clientID, preset.ID)
	if err != nil {
		t.Fatalf("SubmitSettlement() 2nd error = %v", err)
	}
	if order2.SettlementNo == order.SettlementNo {
		t.Fatal("settlement no should be unique")
	}
	// 冲正：本次完成状态迁移
	reversed, replayed, err := svc.ReverseSettlement(ctx, order.SettlementNo)
	if err != nil {
		t.Fatalf("ReverseSettlement() error = %v", err)
	}
	if replayed {
		t.Fatal("first reverse should not be a replay")
	}
	if reversed.Status != constants.SettlementReversed {
		t.Fatalf("status = %s, want reversed", reversed.Status)
	}
	// 重复冲正：幂等返回首次冲正结果，不再报错、不发生二次迁移
	again, replayed, err := svc.ReverseSettlement(ctx, order.SettlementNo)
	if err != nil {
		t.Fatalf("duplicate ReverseSettlement() should be idempotent, got error = %v", err)
	}
	if !replayed {
		t.Fatal("duplicate reverse should be a replay")
	}
	if again.ReversedAt == nil || !again.ReversedAt.Equal(*reversed.ReversedAt) {
		t.Fatalf("replayed ReversedAt = %v, want first result %v", again.ReversedAt, reversed.ReversedAt)
	}
}

func TestSettlementService_ReverseNotToday(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	svc, _ := newSettlementSvc(t, db)
	// 昨日 23:00（Asia/Shanghai 口径之外）结算的单不允许冲正
	yesterday := time.Now().Add(-25 * time.Hour)
	order := seedSettledOrder(t, db, clientID, personID, "GB-OLD-1", yesterday, 500)
	_, _, err := svc.ReverseSettlement(context.Background(), order.SettlementNo)
	if err == nil {
		t.Fatal("expected not-today error")
	}
	appErr, ok := err.(*util.AppError)
	if !ok || appErr.Code != constants.CodeReverseNotToday {
		t.Fatalf("error = %v, want CodeReverseNotToday", err)
	}
	// 状态未被迁移
	got, _ := svc.GetOrder(context.Background(), order.SettlementNo)
	if got.Status != constants.SettlementSettled {
		t.Fatalf("status = %s, want still settled", got.Status)
	}
}

func TestSettlementService_ReverseInvalidState(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	svc, _ := newSettlementSvc(t, db)
	now := time.Now()
	failed := model.SettlementOrder{
		SettlementNo: "GB-FAILED-1", BatchID: 1, InsuredPersonID: personID, PresettlementID: 1,
		ClientID: clientID, Status: constants.SettlementFailed, TotalAmount: 100, SettledAt: &now,
	}
	if err := db.Create(&failed).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err := svc.ReverseSettlement(context.Background(), failed.SettlementNo)
	appErr, ok := err.(*util.AppError)
	if !ok || appErr.Code != constants.CodeReverseInvalidState {
		t.Fatalf("error = %v, want CodeReverseInvalidState", err)
	}
	if _, _, err := svc.ReverseSettlement(context.Background(), "GB-NOT-EXIST"); err == nil {
		t.Fatal("expected not found error")
	}
}

func TestSettlementService_ConcurrentReverseOnlyOnce(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	svc, _ := newSettlementSvc(t, db)
	order := seedSettledOrder(t, db, clientID, personID, "GB-RACE-1", time.Now(), 800)

	const workers = 8
	var wg sync.WaitGroup
	results := make(chan bool, workers) // replayed 标记
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, replayed, err := svc.ReverseSettlement(context.Background(), order.SettlementNo)
			if err != nil {
				errs <- err
				return
			}
			results <- replayed
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent reverse should not error (idempotent), got %v", err)
	}
	migrated := 0
	for replayed := range results {
		if !replayed {
			migrated++
		}
	}
	if migrated != 1 {
		t.Fatalf("state migrated %d times, want exactly 1", migrated)
	}
	got, err := svc.GetOrder(context.Background(), order.SettlementNo)
	if err != nil || got.Status != constants.SettlementReversed {
		t.Fatalf("final order = %+v, err = %v", got, err)
	}
}

func TestSettlementService_ReverseExcludedFromReconciliation(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	svc, recon := newSettlementSvc(t, db)
	keep := seedSettledOrder(t, db, clientID, personID, "GB-KEEP-1", time.Now(), 1000)
	reverse := seedSettledOrder(t, db, clientID, personID, "GB-REVERSE-1", time.Now(), 600)
	_ = keep

	// 冲正前对账：2 笔 / 1600 / 成功 2
	before, err := recon.Daily(ctx, 0)
	if err != nil {
		t.Fatalf("Daily() error = %v", err)
	}
	if before.TotalCount != 2 || before.TotalAmount != 1600 || before.SuccessCount != 2 {
		t.Fatalf("before = %+v, want 2/1600/2", before)
	}
	// 冲正其中一笔
	if _, _, err := svc.ReverseSettlement(ctx, reverse.SettlementNo); err != nil {
		t.Fatalf("ReverseSettlement() error = %v", err)
	}
	// 冲正后：总笔数、总金额、成功笔数均剔除冲正单（闭环内已同步刷新）
	after, err := recon.Daily(ctx, 0)
	if err != nil {
		t.Fatalf("Daily() after error = %v", err)
	}
	if after.TotalCount != 1 || after.TotalAmount != 1000 || after.SuccessCount != 1 {
		t.Fatalf("after = %+v, want 1/1000/1", after)
	}
	// 冲正事务内已刷新存储记录，与重新计算的口径一致
	stored, err := repository.NewDailyReconciliationRepository(db).FindByDate(util.TodayDate())
	if err != nil {
		t.Fatalf("FindByDate() error = %v", err)
	}
	if stored.TotalCount != 1 || stored.TotalAmount != 1000 || stored.SuccessCount != 1 {
		t.Fatalf("stored = %+v, want 1/1000/1", stored)
	}
	// 历史结算查询仍保留冲正单
	orders, total, err := svc.ListOrders(ctx, clientID, "", 1, 20)
	if err != nil || total != 2 || len(orders) != 2 {
		t.Fatalf("history list = %v/%v, err = %v, want 2 orders kept", total, len(orders), err)
	}
	reversedOnly, total, err := svc.ListOrders(ctx, clientID, constants.SettlementReversed, 1, 20)
	if err != nil || total != 1 || reversedOnly[0].SettlementNo != reverse.SettlementNo {
		t.Fatalf("reversed history = %v/%v, err = %v", total, len(reversedOnly), err)
	}
}
