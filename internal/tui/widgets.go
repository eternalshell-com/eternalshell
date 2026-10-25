package tui

import (
	"math"
	"strings"
)

// This file holds the pure helpers behind the small graphics: usage bars and
// sparklines. They only turn numbers into text; colors are applied by the caller.

// usageLevel says how full something is, for the color of a bar.
type usageLevel int

const (
	levelLow  usageLevel = iota // green
	levelMid                    // yellow
	levelHigh                   // red
)

// barLevel: green below 70 %, yellow below 90 %, red from there.
func barLevel(percent float64) usageLevel {
	switch {
	case percent < 70:
		return levelLow
	case percent < 90:
		return levelMid
	default:
		return levelHigh
	}
}

// bar returns the filled and the empty part of a bar of the given width, for
// example "███" and "░░░░░" for 37 % of 8 cells. The percentage is clamped to
// 0..100; a width of 0 or less gives two empty strings.
func bar(percent float64, width int) (filled, empty string) {
	if width <= 0 {
		return "", ""
	}
	if math.IsNaN(percent) {
		percent = 0
	}
	percent = math.Min(math.Max(percent, 0), 100)
	n := int(math.Round(percent / 100 * float64(width)))
	return strings.Repeat("█", n), strings.Repeat("░", width-n)
}

// sparkBlocks are the eight heights of a sparkline cell.
var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// sparkline draws the last `width` values as one row of blocks, newest on the
// right. ceil is the value of a full block; larger values are cut off. Fewer
// values than `width` are padded with spaces on the left: no data is a space,
// a value of zero is the lowest block.
func sparkline(values []float64, width int, ceil float64) string {
	if width <= 0 {
		return ""
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	out := make([]rune, 0, width)
	for i := len(values); i < width; i++ {
		out = append(out, ' ')
	}
	for _, v := range values {
		out = append(out, sparkBlocks[sparkIndex(v, ceil)])
	}
	return string(out)
}

func sparkIndex(v, ceil float64) int {
	if ceil <= 0 || math.IsNaN(v) || v <= 0 {
		return 0
	}
	i := int(v / ceil * float64(len(sparkBlocks)))
	return min(i, len(sparkBlocks)-1)
}

// downsample folds the values into at most n buckets, each holding the highest
// value of its part, so a short spike stays visible. The newest value ends up in
// the last bucket. With n or fewer values nothing changes.
func downsample(values []float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	if len(values) <= n {
		return append([]float64(nil), values...)
	}
	out := make([]float64, n)
	for i := range out {
		from := i * len(values) / n
		to := (i + 1) * len(values) / n
		peak := values[from]
		for _, v := range values[from:to] {
			peak = math.Max(peak, v)
		}
		out[i] = peak
	}
	return out
}

// sumSeries adds several series. They are lined up at their newest end, so a
// container that started later simply has fewer old values.
func sumSeries(series ...[]float64) []float64 {
	longest := 0
	for _, s := range series {
		longest = max(longest, len(s))
	}
	if longest == 0 {
		return nil
	}
	out := make([]float64, longest)
	for _, s := range series {
		for k := 1; k <= len(s); k++ {
			out[longest-k] += s[len(s)-k]
		}
	}
	return out
}

// historyLen is how many samples are kept per container or process: 30 ticks
// of 2 seconds are about a minute.
const historyLen = 30

// appendSample adds a value and keeps only the newest historyLen values.
func appendSample(s []float64, v float64) []float64 {
	s = append(s, v)
	if len(s) > historyLen {
		n := copy(s, s[len(s)-historyLen:])
		s = s[:n]
	}
	return s
}

// sparkWidth is the width of the trend column.
const sparkWidth = 8

// trendCell draws a CPU history as a trend cell. 100 is one full core, so an
// idle container stays flat; a series with a higher peak (several cores) is
// scaled to that peak.
func trendCell(series []float64) string {
	ceil := 100.0
	for _, v := range series {
		ceil = math.Max(ceil, v)
	}
	return sparkline(downsample(series, sparkWidth), sparkWidth, ceil)
}
