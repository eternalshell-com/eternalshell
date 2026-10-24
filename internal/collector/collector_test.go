package collector

import "testing"

func TestTop(t *testing.T) {
	in := []Process{
		{PID: 1, Name: "a", CPU: 5},
		{PID: 2, Name: "b", CPU: 380},
		{PID: 3, Name: "c", CPU: 5},
		{PID: 4, Name: "d", CPU: 0},
	}

	got := Top(in, 3)

	wantPIDs := []int32{2, 1, 3} // 380 before 5; on a tie the lower PID comes first
	if len(got) != len(wantPIDs) {
		t.Fatalf("len = %d, want %d", len(got), len(wantPIDs))
	}
	for i, pid := range wantPIDs {
		if got[i].PID != pid {
			t.Errorf("got[%d].PID = %d, want %d", i, got[i].PID, pid)
		}
	}
	if in[0].PID != 1 {
		t.Error("Top must not modify its input")
	}
}

func TestSkipMount(t *testing.T) {
	tests := []struct {
		path, fstype string
		want         bool
	}{
		{"/", "ext4", false},
		{"/boot", "ext4", false},
		{"/boot/efi", "vfat", true},
		{"/snap/core/1", "squashfs", true},
	}
	for _, tt := range tests {
		if got := skipMount(tt.path, tt.fstype); got != tt.want {
			t.Errorf("skipMount(%q, %q) = %v, want %v", tt.path, tt.fstype, got, tt.want)
		}
	}
}

func TestTopMoreThanAvailable(t *testing.T) {
	got := Top([]Process{{PID: 1}}, 5)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}
