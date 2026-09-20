package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
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
	// sqlite :memory: 每个连接独立库，限单连接保证事务/并发测试确定性。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
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

func TestSettlementService_SubmitAndReverse(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	insurance := NewInsuranceService(repository.NewInsuredPersonRepository(db), testLogger())
	batchRepo := repository.NewUploadBatchRepository(db)
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
	svc := NewSettlementService(
		repository.NewPresettlementRepository(db), repository.NewSettlementOrderRepository(db),
		feeRepo, batchRepo, insurance, util.NewSettlementCalculator(), testLogger(),
	)
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
	// 冲正
	reversed, duplicated, err := svc.ReverseSettlement(ctx, order.SettlementNo)
	if err != nil {
		t.Fatalf("ReverseSettlement() error = %v", err)
	}
	if duplicated {
		t.Fatal("first reverse should not be duplicated")
	}
	if reversed.Status != constants.SettlementReversed {
		t.Fatalf("status = %s, want reversed", reversed.Status)
	}
	// 重复冲正：返回首次冲正结果（幂等），不再迁移状态
	again, duplicated, err := svc.ReverseSettlement(ctx, order.SettlementNo)
	if err != nil {
		t.Fatalf("ReverseSettlement() duplicated error = %v", err)
	}
	if !duplicated {
		t.Fatal("second reverse should be duplicated")
	}
	if again.ReversedAt == nil || !again.ReversedAt.Equal(*reversed.ReversedAt) {
		t.Fatalf("reversed_at changed: first=%v again=%v", reversed.ReversedAt, again.ReversedAt)
	}
}

// seedSettledOrder 直接落库一笔指定结算时间的已结算单（绕过完整结算链路）。
func seedSettledOrder(t *testing.T, db *gorm.DB, clientID, personID uint, settledAt time.Time, amount float64) model.SettlementOrder {
	t.Helper()
	order := model.SettlementOrder{
		SettlementNo: util.SettlementNo(time.Now().UnixNano()), BatchID: 1, InsuredPersonID: personID,
		PresettlementID: 1, ClientID: clientID, Status: constants.SettlementSettled,
		TotalAmount: amount, InsurancePayAmount: amount * 0.7, SettledAt: &settledAt,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return order
}

func newSettlementSvc(db *gorm.DB) *SettlementService {
	insurance := NewInsuranceService(repository.NewInsuredPersonRepository(db), testLogger())
	return NewSettlementService(
		repository.NewPresettlementRepository(db), repository.NewSettlementOrderRepository(db),
		repository.NewFeeItemRepository(db), repository.NewUploadBatchRepository(db),
		insurance, util.NewSettlementCalculator(), testLogger(),
	)
}

// 冲正闭环：仅当天已结算单可冲正，其余状态一律拒绝。
func TestSettlementService_ReverseRejectsNonSettled(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	svc := newSettlementSvc(db)
	todayStart, _, err := util.DayRange(util.TodayDate())
	if err != nil {
		t.Fatalf("DayRange() error = %v", err)
	}
	order := seedSettledOrder(t, db, clientID, personID, todayStart.Add(2*time.Hour), 500)
	// 置为待人工处理：非 settled 状态禁止冲正
	if err := db.Model(&model.SettlementOrder{}).Where("id = ?", order.ID).
		Update("status", constants.SettlementPendingManual).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.ReverseSettlement(ctx, order.SettlementNo)
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != constants.CodeReverseNotSettled {
		t.Fatalf("err = %v, want CodeReverseNotSettled", err)
	}
	// 状态未被半更新
	fresh, _ := repository.NewSettlementOrderRepository(db).FindByNo(order.SettlementNo)
	if fresh.Status != constants.SettlementPendingManual || fresh.ReversedAt != nil {
		t.Fatalf("order half updated: %+v", fresh)
	}
}

// 冲正闭环：跨日（非当天）已结算单禁止冲正。
func TestSettlementService_ReverseRejectsNotToday(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	svc := newSettlementSvc(db)
	todayStart, _, err := util.DayRange(util.TodayDate())
	if err != nil {
		t.Fatalf("DayRange() error = %v", err)
	}
	yesterday := todayStart.Add(-2 * time.Hour) // 昨天 22:00（Asia/Shanghai）
	order := seedSettledOrder(t, db, clientID, personID, yesterday, 800)
	_, _, err = svc.ReverseSettlement(ctx, order.SettlementNo)
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != constants.CodeReverseNotToday {
		t.Fatalf("err = %v, want CodeReverseNotToday", err)
	}
	fresh, _ := repository.NewSettlementOrderRepository(db).FindByNo(order.SettlementNo)
	if fresh.Status != constants.SettlementSettled || fresh.ReversedAt != nil {
		t.Fatalf("order half updated: %+v", fresh)
	}
}

// 冲正闭环：并发到达也只完成一次状态迁移，其余请求拿到首次冲正结果。
func TestSettlementService_ReverseConcurrentOnce(t *testing.T) {
	db := newTestDB(t)
	clientID, personID := seedData(t, db)
	ctx := context.Background()
	svc := newSettlementSvc(db)
	todayStart, _, err := util.DayRange(util.TodayDate())
	if err != nil {
		t.Fatalf("DayRange() error = %v", err)
	}
	order := seedSettledOrder(t, db, clientID, personID, todayStart.Add(3*time.Hour), 666)

	const workers = 8
	var wg sync.WaitGroup
	firstWins := int32(0)
	dupWins := int32(0)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, duplicated, err := svc.ReverseSettlement(ctx, order.SettlementNo)
			if err != nil {
				errs <- err
				return
			}
			if got.Status != constants.SettlementReversed {
				errs <- errors.New("status not reversed")
				return
			}
			if duplicated {
				atomic.AddInt32(&dupWins, 1)
			} else {
				atomic.AddInt32(&firstWins, 1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent reverse error = %v", err)
	}
	if firstWins != 1 {
		t.Fatalf("first transition count = %d, want exactly 1", firstWins)
	}
	if dupWins != workers-1 {
		t.Fatalf("duplicated count = %d, want %d", dupWins, workers-1)
	}
	// 仓储层原子迁移兜底：直接再次 MarkReversed 不得影响任何行
	affected, err := repository.NewSettlementOrderRepository(db).MarkReversed(order.SettlementNo, time.Now())
	if err != nil {
		t.Fatalf("MarkReversed() error = %v", err)
	}
	if affected != 0 {
		t.Fatalf("MarkReversed affected = %d, want 0", affected)
	}
	// 历史结算查询仍保留已冲正单
	orders, total, err := repository.NewSettlementOrderRepository(db).List(clientID, "", 1, 10)
	if err != nil || total != 1 || len(orders) != 1 {
		t.Fatalf("history list lost reversed order: total=%d err=%v", total, err)
	}
	if orders[0].Status != constants.SettlementReversed {
		t.Fatalf("history status = %s, want reversed", orders[0].Status)
	}
}
