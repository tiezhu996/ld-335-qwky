package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/blueship581/gbinsureapi/internal/constants"
	"github.com/blueship581/gbinsureapi/internal/model"
	"github.com/blueship581/gbinsureapi/internal/repository"
	"github.com/blueship581/gbinsureapi/internal/util"
	"gorm.io/gorm"
)

// SettlementService 结算服务：预结算计算、正式结算、当日冲正（复用 SettlementOrderRepository）。
type SettlementService struct {
	presetRepo  *repository.PresettlementRepository
	orderRepo   *repository.SettlementOrderRepository
	feeRepo     *repository.FeeItemRepository
	batchRepo   *repository.UploadBatchRepository
	insurance   *InsuranceService
	recon       *ReconciliationService
	calculator  *util.SettlementCalculator
	log         *slog.Logger
}

// NewSettlementService 构造结算服务。
func NewSettlementService(presetRepo *repository.PresettlementRepository, orderRepo *repository.SettlementOrderRepository, feeRepo *repository.FeeItemRepository, batchRepo *repository.UploadBatchRepository, insurance *InsuranceService, recon *ReconciliationService, calculator *util.SettlementCalculator, log *slog.Logger) *SettlementService {
	return &SettlementService{presetRepo: presetRepo, orderRepo: orderRepo, feeRepo: feeRepo, batchRepo: batchRepo, insurance: insurance, recon: recon, calculator: calculator, log: log}
}

// CalculatePresettlement 预结算计算（支持多次比对，不落库状态机）。
func (s *SettlementService) CalculatePresettlement(ctx context.Context, batchID uint) (*model.Presettlement, error) {
	batch, err := s.batchRepo.FindByID(batchID)
	if err != nil {
		if errors.Is(err, util.ErrNotFound) {
			return nil, util.NotFoundError("费用批次（UploadBatch）不存在", err)
		}
		return nil, err
	}
	person, err := s.insurance.GetByID(ctx, batch.InsuredPersonID)
	if err != nil {
		return nil, err
	}
	items, err := s.feeRepo.ListByBatch(batchID)
	if err != nil {
		return nil, util.LogError(s.log, constants.LOG_PRESETTLEMENT_FAILED, fmt.Errorf("list fee items: %w", err))
	}
	inputs := make([]util.FeeInput, 0, len(items))
	for _, it := range items {
		inputs = append(inputs, util.FeeInput{
			ItemCode: it.ItemCode, ItemName: it.ItemName,
			MedicalCategory: it.MedicalCategory, Amount: it.Amount,
		})
	}
	result, err := s.calculator.Calculate(person.InsuranceType, person.PersonalBalance, inputs)
	if err != nil {
		return nil, util.LogError(s.log, constants.LOG_PRESETTLEMENT_FAILED, fmt.Errorf("calculate: %w", err))
	}
	preset := &model.Presettlement{
		BatchID: batch.ID, InsuredPersonID: person.ID,
		TotalAmount: result.TotalAmount, InsurancePayAmount: result.InsurancePayAmount,
		PersonalAccountAmount: result.PersonalAccountAmount, SelfPayAmount: result.SelfPayAmount,
		Deductible: result.Deductible, ReimbursementRatio: result.ReimbursementRatio,
		ResultPayload: MarshalResult(result),
	}
	if err := s.presetRepo.Create(preset); err != nil {
		return nil, util.LogError(s.log, constants.LOG_PRESETTLEMENT_FAILED, fmt.Errorf("create presettlement: %w", err))
	}
	s.log.InfoContext(ctx, constants.LOG_PRESETTLEMENT_CALCULATED, "preset_id", preset.ID, "batch_no", batch.BatchNo)
	return preset, nil
}

// SubmitSettlement 正式结算：确认预结算后生成唯一结算单号。
func (s *SettlementService) SubmitSettlement(ctx context.Context, clientID, presetID uint) (*model.SettlementOrder, error) {
	preset, err := s.presetRepo.FindByID(presetID)
	if err != nil {
		if errors.Is(err, util.ErrNotFound) {
			return nil, util.NotFoundError(constants.MsgPresettleNotFound, err)
		}
		return nil, err
	}
	// 唯一结算单号（冲突重试）
	no := ""
	for i := 0; i < 5; i++ {
		seq, _ := s.orderRepo.Count()
		candidate := util.SettlementNo(seq + 1 + int64(i))
		exists, err := s.orderRepo.ExistsByNo(candidate)
		if err != nil {
			return nil, util.LogError(s.log, constants.LOG_SETTLEMENT_FAILED, fmt.Errorf("check settlement no: %w", err))
		}
		if !exists {
			no = candidate
			break
		}
	}
	if no == "" {
		return nil, util.InternalError(constants.MsgSettlementNoUnique, errors.New("settlement no conflict"))
	}
	now := time.Now()
	order := &model.SettlementOrder{
		SettlementNo: no, BatchID: preset.BatchID, InsuredPersonID: preset.InsuredPersonID,
		PresettlementID: preset.ID, ClientID: clientID, Status: constants.SettlementSettled,
		TotalAmount: preset.TotalAmount, InsurancePayAmount: preset.InsurancePayAmount,
		SettledAt: &now,
	}
	if err := s.orderRepo.Create(order); err != nil {
		return nil, util.LogError(s.log, constants.LOG_SETTLEMENT_FAILED, fmt.Errorf("create settlement order: %w", err))
	}
	s.log.InfoContext(ctx, constants.LOG_SETTLEMENT_SUBMITTED, "settlement_no", no, "preset_id", presetID)
	return order, nil
}

// errReverseRaceLost 冲正竞争失败哨兵：原子条件更新命中 0 行，回滚后重读分类。
var errReverseRaceLost = errors.New("settlement reverse race lost")

// ReverseSettlement 当日冲正（全额回退）。
// 返回 (order, replayed, err)：
//   - replayed=false：本次请求完成了唯一一次 settled -> reversed 状态迁移；
//   - replayed=true ：重复或并发请求，幂等返回首次冲正结果，不发生二次迁移。
//
// 闭环约束：仅当日已结算单可冲正；状态迁移与当日对账刷新在同一事务提交，
// 任一失败整体回滚，不会留下半更新；冲正单从日终口径剔除，历史查询仍保留。
func (s *SettlementService) ReverseSettlement(ctx context.Context, settlementNo string) (*model.SettlementOrder, bool, error) {
	// 预读：快速路径与精确错误分类（并发正确性由事务内条件更新兜底）。
	order, err := s.orderRepo.FindByNo(settlementNo)
	if err != nil {
		if errors.Is(err, util.ErrNotFound) {
			return nil, false, util.NotFoundError(constants.MsgSettlementNotFound, err)
		}
		return nil, false, err
	}
	if order.Status == constants.SettlementReversed {
		// 重复请求：幂等返回首次冲正结果（ReversedAt 为首次冲正时间）。
		s.log.InfoContext(ctx, constants.LOG_SETTLEMENT_REVERSE_REPLAY, "settlement_no", settlementNo, "reversed_at", order.ReversedAt)
		return order, true, nil
	}
	if err := checkReversable(order); err != nil {
		return nil, false, err
	}

	// 事务：原子状态迁移 + 当日对账刷新，一次提交，失败整体回滚。
	now := time.Now()
	start, end := util.TodayBounds()
	txErr := s.orderRepo.Transaction(func(tx *gorm.DB) error {
		affected, err := s.orderRepo.MarkReversedToday(tx, settlementNo, start, end, now)
		if err != nil {
			return fmt.Errorf("mark reversed: %w", err)
		}
		if affected == 0 {
			return errReverseRaceLost
		}
		if _, err := s.recon.RefreshDailyInTx(ctx, tx, util.TodayDate()); err != nil {
			return fmt.Errorf("refresh daily reconciliation: %w", err)
		}
		return nil
	})
	if txErr != nil {
		if errors.Is(txErr, errReverseRaceLost) {
			s.log.InfoContext(ctx, constants.LOG_SETTLEMENT_REVERSE_RACE, "settlement_no", settlementNo)
			return s.replayOrConflict(ctx, settlementNo)
		}
		return nil, false, util.LogError(s.log, constants.LOG_SETTLEMENT_REVERSE_FAILED, txErr)
	}
	order.Status = constants.SettlementReversed
	order.ReversedAt = &now
	s.log.InfoContext(ctx, constants.LOG_SETTLEMENT_REVERSED, "settlement_no", settlementNo)
	return order, false, nil
}

// checkReversable 冲正前置校验：仅当日已结算（settled）单可冲正。
func checkReversable(order *model.SettlementOrder) error {
	if order.Status != constants.SettlementSettled {
		return util.NewAppError(constants.CodeReverseInvalidState, 409, constants.MsgReverseInvalidState,
			fmt.Errorf("SettlementOrder[no=%s] status=%s", order.SettlementNo, order.Status))
	}
	if !util.IsSettledToday(order.SettledAt) {
		return util.NewAppError(constants.CodeReverseNotToday, 409, constants.MsgReverseNotToday,
			fmt.Errorf("SettlementOrder[no=%s] settled_at=%v not today", order.SettlementNo, order.SettledAt))
	}
	return nil
}

// replayOrConflict 条件更新未命中后的重读分类：已被并发冲正则幂等返回首次结果，
// 否则按当前状态给出冲突原因。
func (s *SettlementService) replayOrConflict(ctx context.Context, settlementNo string) (*model.SettlementOrder, bool, error) {
	order, err := s.orderRepo.FindByNo(settlementNo)
	if err != nil {
		if errors.Is(err, util.ErrNotFound) {
			return nil, false, util.NotFoundError(constants.MsgSettlementNotFound, err)
		}
		return nil, false, err
	}
	if order.Status == constants.SettlementReversed {
		s.log.InfoContext(ctx, constants.LOG_SETTLEMENT_REVERSE_REPLAY, "settlement_no", settlementNo, "reversed_at", order.ReversedAt)
		return order, true, nil
	}
	if err := checkReversable(order); err != nil {
		return nil, false, err
	}
	// 已结算且当日却更新 0 行：异常分支，按内部错误处理。
	return nil, false, util.InternalError(constants.MsgInternalError,
		fmt.Errorf("SettlementOrder[no=%s] reverse lost without state change", settlementNo))
}

// ListOrders 分页查询结算单。
func (s *SettlementService) ListOrders(ctx context.Context, clientID uint, status string, page, pageSize int) ([]model.SettlementOrder, int64, error) {
	return s.orderRepo.List(clientID, status, page, pageSize)
}

// GetOrder 查询结算单详情。
func (s *SettlementService) GetOrder(ctx context.Context, settlementNo string) (*model.SettlementOrder, error) {
	order, err := s.orderRepo.FindByNo(settlementNo)
	if err != nil {
		if errors.Is(err, util.ErrNotFound) {
			return nil, util.NotFoundError(constants.MsgSettlementNotFound, err)
		}
		return nil, err
	}
	return order, nil
}

// ListPresettlements 查询批次预结算记录（多次比对）。
func (s *SettlementService) ListPresettlements(ctx context.Context, batchID uint) ([]model.Presettlement, error) {
	return s.presetRepo.ListByBatch(batchID)
}
