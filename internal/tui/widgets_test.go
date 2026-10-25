package tui

import (
	"reflect"
	"testing"
)

func TestBar(t *testing.T) {
	tests := []struct {
		percent float64
		width   int
		filled  int
		empty   int
	}{
		{0, 8, 0, 8},
		{12, 8, 1, 7}, // 0.96 cells round to 1
		{50, 8, 4, 4},
		{37.5, 8, 3, 5},
		{100, 8, 8, 0},
		{250, 8, 8, 0}, // clamped
		{-20, 8, 0, 8}, // clamped
		{49, 10, 5, 5}, // 4.9 rounds up
		{100, 1, 1, 0},
		{0, 0, 0, 0},
		{50, -3, 0, 0},
	}
	for _, tt := range tests {
		f, e := bar(tt.percent, tt.width)
		if got := len([]rune(f)); got != tt.filled {
			t.Errorf("bar(%v, %d): filled = %d, want %d", tt.percent, tt.width, got, tt.filled)
		}
		if got := len([]rune(e)); got != tt.empty {
			t.Errorf("bar(%v, %d): empty = %d, want %d", tt.percent, tt.width, got, tt.empty)
		}
	}
	if f, e := bar(75, 4); f != "███" || e != "░" {
		t.Errorf("bar(75, 4) = %q %q", f, e)
	}
	// not a number counts as empty
	if f, _ := bar(nanValue(), 8); f != "" {
		t.Errorf("NaN should give an empty bar, got %q", f)
	}
}

func nanValue() float64 {
	zero := 0.0
	return zero / zero
}

func TestBarLevel(t *testing.T) {
	tests := []struct {
		p    float64
		want usageLevel
	}{
		{0, levelLow}, {69.9, levelLow}, {70, levelMid}, {89.9, levelMid}, {90, levelHigh}, {100, levelHigh}, {350, levelHigh},
	}
	for _, tt := range tests {
		if got := barLevel(tt.p); got != tt.want {
			t.Errorf("barLevel(%v) = %v, want %v", tt.p, got, tt.want)
		}
	}
}

func TestSparkline(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		width  int
		ceil   float64
		want   string
	}{
		{"no data is blank", nil, 4, 100, "    "},
		{"zero is the lowest block", []float64{0}, 3, 100, "  ▁"},
		{"fewer values are padded on the left", []float64{100, 0}, 5, 100, "   █▁"},
		{"full width", []float64{0, 25, 50, 75, 100}, 5, 100, "▁▃▅▇█"},
		{"only the newest values", []float64{100, 100, 0, 100}, 2, 100, "▁█"},
		{"above ceil is cut off", []float64{500, 1000}, 2, 100, "██"},
		{"negative counts as zero", []float64{-5}, 1, 100, "▁"},
		{"a higher ceil scales down", []float64{50, 200}, 2, 400, "▂▅"},
		{"zero ceil does not divide", []float64{10}, 1, 0, "▁"},
		{"width zero", []float64{1, 2}, 0, 100, ""},
	}
	for _, tt := range tests {
		if got := sparkline(tt.values, tt.width, tt.ceil); got != tt.want {
			t.Errorf("%s: sparkline = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestDownsample(t *testing.T) {
	in := []float64{1, 9, 2, 3, 8, 4}
	if got, want := downsample(in, 3), []float64{9, 3, 8}; !reflect.DeepEqual(got, want) {
		t.Errorf("max per bucket: got %v, want %v", got, want)
	}
	if got := downsample(in, 6); !reflect.DeepEqual(got, in) {
		t.Errorf("same size should not change anything, got %v", got)
	}
	if got := downsample(in, 10); !reflect.DeepEqual(got, in) {
		t.Errorf("fewer values than buckets stay as they are, got %v", got)
	}
	if got := downsample(in, 0); got != nil {
		t.Errorf("no buckets: got %v", got)
	}
	// the newest value always lands in the last bucket, uneven sizes included
	seven := []float64{0, 0, 0, 0, 0, 0, 5}
	if got := downsample(seven, 3); got[2] != 5 || got[0] != 0 {
		t.Errorf("newest value must be in the last bucket, got %v", got)
	}
	// the input is not changed
	if in[1] != 9 {
		t.Error("downsample changed its input")
	}
}

func TestSumSeries(t *testing.T) {
	got := sumSeries([]float64{1, 2, 3}, []float64{10, 20})
	if want := []float64{1, 12, 23}; !reflect.DeepEqual(got, want) {
		t.Errorf("lined up at the newest end: got %v, want %v", got, want)
	}
	if got := sumSeries(nil, nil); got != nil {
		t.Errorf("nothing to add: got %v", got)
	}
	if got := sumSeries([]float64{4}); !reflect.DeepEqual(got, []float64{4}) {
		t.Errorf("one series: got %v", got)
	}
}

func TestAppendSampleKeepsTheNewest(t *testing.T) {
	var s []float64
	for i := 1; i <= historyLen+5; i++ {
		s = appendSample(s, float64(i))
	}
	if len(s) != historyLen || s[0] != 6 || s[len(s)-1] != float64(historyLen+5) {
		t.Errorf("len %d first %v last %v", len(s), s[0], s[len(s)-1])
	}
}

func TestTrendCell(t *testing.T) {
	if got := trendCell(nil); got != "        " {
		t.Errorf("no history: %q", got)
	}
	idle := make([]float64, 30)
	if got := trendCell(idle); got != "▁▁▁▁▁▁▁▁" {
		t.Errorf("an idle container stays flat: %q", got)
	}
	// a short spike survives the folding into 8 cells
	spiky := make([]float64, 30)
	spiky[10] = 100
	got := []rune(trendCell(spiky))
	if len(got) != sparkWidth {
		t.Fatalf("width %d, want %d", len(got), sparkWidth)
	}
	hasPeak := false
	for _, r := range got {
		hasPeak = hasPeak || r == '█'
	}
	if !hasPeak {
		t.Errorf("the spike must stay visible: %q", string(got))
	}
}
