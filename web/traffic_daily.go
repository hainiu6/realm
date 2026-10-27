package main

import (
	"math/bits"
	"time"
)

type dailyTraffic struct {
	Upload     uint64 `json:"upload"`
	Download   uint64 `json:"download"`
	Incomplete bool   `json:"incomplete,omitempty"`
}

func (m *TrafficMonitor) nowTime() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

func dailyDates(now time.Time) (string, string) {
	return now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02")
}

func pruneDaily(usage *trafficUsage, now time.Time) {
	if usage.Days == nil {
		usage.Days = make(map[string]dailyTraffic)
	}
	today, yesterday := dailyDates(now)
	for date := range usage.Days {
		if date != today && date != yesterday {
			delete(usage.Days, date)
		}
	}
}

func addDaily(usage *trafficUsage, date string, up, down uint64) {
	day := usage.Days[date]
	day.Upload += up
	day.Download += down
	usage.Days[date] = day
}

// A short sampling interval crossing local midnight is split in proportion to
// elapsed time. Long cross-day outages cannot be reconstructed from one kernel
// counter: retain those bytes in lifetime totals and mark the daily view partial.
func recordDaily(usage *trafficUsage, up, down uint64, now time.Time) {
	previousAt := usage.DailySampleAt
	usage.DailySampleAt = now.UnixMilli()
	// Old-format records have no daily baseline. Never label their lifetime
	// history (or bytes collected before this upgrade) as today's traffic.
	if previousAt == 0 || (up == 0 && down == 0) {
		return
	}
	today, yesterday := dailyDates(now)
	previous := time.UnixMilli(previousAt).In(now.Location())
	previousDate := previous.Format("2006-01-02")
	if previousDate == today {
		addDaily(usage, today, up, down)
		return
	}
	interval := now.Sub(previous)
	if previousDate == yesterday && interval > 0 && interval <= time.Minute {
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		before := midnight.Sub(previous)
		oldUp := proportionalBytes(up, before, interval)
		oldDown := proportionalBytes(down, before, interval)
		addDaily(usage, yesterday, oldUp, oldDown)
		addDaily(usage, today, up-oldUp, down-oldDown)
		return
	}
	for _, date := range []string{today, yesterday} {
		day := usage.Days[date]
		day.Incomplete = true
		usage.Days[date] = day
	}
}

func proportionalBytes(total uint64, part, whole time.Duration) uint64 {
	if part <= 0 {
		return 0
	}
	if part >= whole {
		return total
	}
	hi, lo := bits.Mul64(total, uint64(part))
	value, _ := bits.Div64(hi, lo, uint64(whole))
	return value
}

func dailyView(usage trafficUsage, now time.Time) (dailyTraffic, dailyTraffic, string) {
	today, yesterday := dailyDates(now)
	current, previous := usage.Days[today], usage.Days[yesterday]
	warning := ""
	if current.Incomplete || previous.Incomplete {
		warning = "跨天采样中断，今日/昨日数据可能不完整；无法按日划分的流量已保留在累计用量中"
	}
	return current, previous, warning
}
