package model

import "time"

// DailyReconciliation 日终对账。
// 口径：TotalCount/TotalAmount/SuccessCount 均已剔除当日冲正单；
// 冲正单单独计入 ReversedCount/ReversedAmount，历史结算查询不受影响。
type DailyReconciliation struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	ReconcileDate   string    `gorm:"size:10;uniqueIndex;not null" json:"reconcile_date"`
	TotalCount      int64     `json:"total_count"`
	TotalAmount     float64   `json:"total_amount"`
	SuccessCount    int64     `json:"success_count"`
	FailCount       int64     `json:"fail_count"`
	AbnormalOrders  int64     `json:"abnormal_orders"`
	ReversedCount   int64     `json:"reversed_count"`
	ReversedAmount  float64   `json:"reversed_amount"`
	CreatedAt       time.Time `json:"created_at"`
}
