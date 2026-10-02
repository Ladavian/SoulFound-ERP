package service

import "time"

const dateLayout = "2006-01-02"

// todayDate 今天（本地时区，随容器 TZ）。
func todayDate() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// monthStart 月初。
func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

// yearStart 年初。
func yearStart(t time.Time) time.Time {
	return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
}

// addMonths 月份加减。
func addMonths(t time.Time, months int) time.Time {
	first := monthStart(t)
	return first.AddDate(0, months, 0)
}

// fmtDate 格式化日期。
func fmtDate(t time.Time) string { return t.Format(dateLayout) }

// monthKey 把日期字符串转成 "2025-10"。
func monthKey(dateStr string) string {
	if len(dateStr) >= 7 {
		return dateStr[:7]
	}
	return dateStr
}

// monthLabel 中文月份标签，如 "2025年10月"。
func monthLabel(key string) string {
	t, err := time.Parse("2006-01", key)
	if err != nil {
		return key
	}
	return t.Format("2006年1月")
}
