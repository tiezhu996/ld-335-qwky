package util

import "time"

func timeNowDate() string {
	return time.Now().Format("20060102")
}

// NowTime 返回当前时间。
func NowTime() time.Time { return time.Now() }

// ShanghaiLoc 返回 Asia/Shanghai 时区（与 DB 会话 TimeZone=Asia/Shanghai 一致）。
func ShanghaiLoc() *time.Location {
	return time.FixedZone("CST", 8*3600)
}

// TodayDate 返回 Asia/Shanghai 时区的今天日期（与 DB 会话 TimeZone=Asia/Shanghai 一致）。
func TodayDate() string {
	return time.Now().In(ShanghaiLoc()).Format("2006-01-02")
}

// DateOf 返回时间 t 在 Asia/Shanghai 时区下的日历日期串（冲正"当日"口径）。
func DateOf(t time.Time) string {
	return t.In(ShanghaiLoc()).Format("2006-01-02")
}

// IsSameDay 判断 t 与今天是否为同一日历日（Asia/Shanghai），冲正仅允许当日单。
func IsSameDay(t time.Time) bool {
	return DateOf(t) == TodayDate()
}

// DayRange 返回某日 [start, end) 的时间范围（Asia/Shanghai），供对账/结算仓储统一取数口径。
func DayRange(date string) (time.Time, time.Time, error) {
	start, err := time.ParseInLocation("2006-01-02", date, ShanghaiLoc())
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, start.AddDate(0, 0, 1), nil
}
