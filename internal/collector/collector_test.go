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

	wantPIDs := []int32{2, 1, 3} // 380 vor 5; bei Gleichstand kleinere PID zuerst
	if len(got) != len(wantPIDs) {
		t.Fatalf("len = %d, want %d", len(got), len(wantPIDs))
	}
	for i, pid := range wantPIDs {
		if got[i].PID != pid {
			t.Errorf("got[%d].PID = %d, want %d", i, got[i].PID, pid)
		}
	}
	if in[0].PID != 1 {
		t.Error("Top darf die Eingabe nicht verändern")
	}
}

func TestTopMoreThanAvailable(t *testing.T) {
	got := Top([]Process{{PID: 1}}, 5)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}
