package util

import "time"

// shanghaiLoc 业务日界统一使用 Asia/Shanghai（与 DB 会话 TimeZone=Asia/Shanghai 一致）。
var shanghaiLoc = time.FixedZone("CST", 8*3600)

func timeNowDate() string {
	return time.Now().Format("20060102")
}

// NowTime 返回当前时间。
func NowTime() time.Time { return time.Now() }

// TodayDate 返回 Asia/Shanghai 时区的今天日期（与 DB 会话 TimeZone=Asia/Shanghai 一致）。
func TodayDate() string {
	return time.Now().In(shanghaiLoc).Format("2006-01-02")
}

// DayBounds 返回 Asia/Shanghai 时区指定日期（2006-01-02）的 [start, end) 区间。
// 返回值统一转为 UTC 瞬时，供 SQL 范围比较（settled_at >= ? AND settled_at < ?），
// 在 PostgreSQL TIMESTAMPTZ 与 SQLite 上语义一致；冲正当日校验与日终对账共用该日界。
func DayBounds(date string) (time.Time, time.Time) {
	t, err := time.ParseInLocation("2006-01-02", date, shanghaiLoc)
	if err != nil {
		// 非法日期串退化为空区间，查询不命中任何记录。
		return time.Time{}, time.Time{}
	}
	start := t.UTC()
	return start, start.AddDate(0, 0, 1)
}

// TodayBounds 返回当日（Asia/Shanghai）的 [start, end) 区间。
func TodayBounds() (time.Time, time.Time) {
	return DayBounds(TodayDate())
}

// IsSettledToday 判断结算时间是否落在当日（Asia/Shanghai），nil 视为非当日。
func IsSettledToday(t *time.Time) bool {
	if t == nil {
		return false
	}
	start, end := TodayBounds()
	at := t.UTC()
	return !at.Before(start) && at.Before(end)
}
