package dto

// DailyReconciliationResponse 日终对账响应。
// 口径：冲正单从 total_count/total_amount/success_count 中剔除，
// 单独以 reversed_count/reversed_amount 呈现，保证剔除可审计。
type DailyReconciliationResponse struct {
	ReconcileDate  string `json:"reconcile_date"`
	TotalCount     int64  `json:"total_count"`
	TotalAmount    string `json:"total_amount"`
	SuccessCount   int64  `json:"success_count"`
	FailCount      int64  `json:"fail_count"`
	AbnormalOrders int64  `json:"abnormal_orders"`
	ReversedCount  int64  `json:"reversed_count"`
	ReversedAmount string `json:"reversed_amount"`
	Caliber        string `json:"caliber"`
}
