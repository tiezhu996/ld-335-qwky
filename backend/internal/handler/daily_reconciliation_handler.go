package handler

import (
	"log/slog"

	"github.com/blueship581/gbinsureapi/internal/constants"
	"github.com/blueship581/gbinsureapi/internal/dto"
	"github.com/blueship581/gbinsureapi/internal/service"
	"github.com/blueship581/gbinsureapi/internal/util"
	"github.com/gin-gonic/gin"
)

// DailyReconciliationHandler 日终对账接口。
type DailyReconciliationHandler struct {
	svc *service.ReconciliationService
	log *slog.Logger
}

// NewDailyReconciliationHandler 构造日终对账接口。
func NewDailyReconciliationHandler(svc *service.ReconciliationService, log *slog.Logger) *DailyReconciliationHandler {
	return &DailyReconciliationHandler{svc: svc, log: log}
}

// Daily 日终对账。
// @Summary 日终对账
// @Description 返回当日总笔数、总金额、成功/失败笔数；冲正单已从总笔数/总金额/成功笔数剔除并单列
// @Tags reconciliations
// @Security ApiKeyAuth
// @Security BearerAuth
// @Param client_id query int false "调用方 ID"
// @Success 200 {object} util.Response
// @Router /api/v1/reconciliations/daily [get]
func (h *DailyReconciliationHandler) Daily(c *gin.Context) {
	clientID := parseUint(c.Query("client_id"))
	rec, err := h.svc.Daily(c.Request.Context(), clientID)
	if err != nil {
		h.log.WarnContext(c.Request.Context(), constants.LOG_RECONCILIATION_FAILED,
			"client_id", clientID, "error", err)
		c.Error(err)
		return
	}
	util.OK(c, dto.DailyReconciliationResponse{
		ReconcileDate:  rec.ReconcileDate,
		TotalCount:     rec.TotalCount,
		TotalAmount:    util.FormatMoney(rec.TotalAmount),
		SuccessCount:   rec.SuccessCount,
		FailCount:      rec.FailCount,
		AbnormalOrders: rec.AbnormalOrders,
		ReversedCount:  rec.ReversedCount,
		ReversedAmount: util.FormatMoney(rec.ReversedAmount),
		Caliber:        constants.MsgReconciliationCaliber,
	})
}

// List 对账记录列表。
// @Summary 对账记录列表
// @Tags reconciliations
// @Security ApiKeyAuth
// @Security BearerAuth
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Success 200 {object} util.Response
// @Router /api/v1/reconciliations [get]
func (h *DailyReconciliationHandler) List(c *gin.Context) {
	page := parseQueryInt(c.Query("page"), 1)
	pageSize := parseQueryInt(c.Query("page_size"), 20)
	items, total, err := h.svc.List(c.Request.Context(), page, pageSize)
	if err != nil {
		c.Error(err)
		return
	}
	util.OK(c, util.PageData{List: items, Total: total, Page: page, Size: pageSize})
}
