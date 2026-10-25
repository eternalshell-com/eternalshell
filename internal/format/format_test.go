package format

import "testing"

func TestBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1.0K"},
		{90 * 1024 * 1024, "90.0M"},
		{3 * 1024 * 1024 * 1024, "3.0G"},
	}
	for _, tt := range tests {
		if got := Bytes(tt.in); got != tt.want {
			t.Errorf("Bytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPercent(t *testing.T) {
	if got := Percent(381.6); got != "382%" {
		t.Errorf("Percent(381.6) = %q, want 382%%", got)
	}
}

func TestBytesPartial(t *testing.T) {
	tests := []struct {
		b       int64
		unknown bool
		want    string
	}{
		{0, false, "0B"},
		{3 << 30, false, "3.0G"},
		{3 << 30, true, "3.0G+?"},
		{0, true, "?"},
	}
	for _, tt := range tests {
		if got := BytesPartial(tt.b, tt.unknown); got != tt.want {
			t.Errorf("BytesPartial(%d, %v) = %q, want %q", tt.b, tt.unknown, got, tt.want)
		}
	}
}
