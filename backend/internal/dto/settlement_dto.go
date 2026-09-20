package dto

// CalculatePresettlementRequest 预结算请求。
type CalculatePresettlementRequest struct {
	BatchID uint `json:"batch_id" binding:"required"`
}

// SubmitSettlementRequest 正式结算请求。
type SubmitSettlementRequest struct {
	PresettlementID uint `json:"presettlement_id" binding:"required"`
}

// ReverseSettlementResponse 冲正响应。
// 重复冲正请求同样返回 200 与本结构：duplicated=true 且 reversed_at 为首次冲正时间。
type ReverseSettlementResponse struct {
	SettlementNo string `json:"settlement_no"`
	Status       string `json:"status"`
	StatusText   string `json:"status_text"`
	TotalAmount  string `json:"total_amount"`
	ReversedAt   string `json:"reversed_at"`
	Duplicated   bool   `json:"duplicated"`
	Message      string `json:"message"`
}
