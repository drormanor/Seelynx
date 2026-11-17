package media

import (
	"testing"

	"seelynx/internal/store"
)

func TestEachLocationsVideoGetsAPlaybackPath(t *testing.T) {
	c := store.Camera{ID: 7, MainURL: "rtsp://cam/main", SubURL: "rtsp://cam/sub", Record: true, Enabled: true}
	got, err := desired(c, Placement{Record: `D:\Video`, Archives: map[int64]string{1: `D:\Video`, 2: "/mnt/nas"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("%v", got)
	}
	main := got["cam_7_main"]
	if !main.Record || main.RecordPath != "D:/Video/%path/%Y-%m-%d_%H-%M-%S-%f" {
		t.Errorf("main: %+v", main)
	}
	if got["cam_7_sub"].Record || got["cam_7_sub"].RecordPath != "" {
		t.Errorf("sub: %+v", got["cam_7_sub"])
	}
	for name, dir := range map[string]string{"cam_7_main_at1": "D:/Video", "cam_7_main_at2": "/mnt/nas"} {
		p := got[name]
		if p.Record || p.Source != "publisher" || p.RecordPath != dir+"/%path/%Y-%m-%d_%H-%M-%S-%f" {
			t.Errorf("%s: %+v", name, p)
		}
	}
	// A disabled camera has nothing.
	c.Enabled = false
	if got, _ := desired(c, Placement{Record: "x", Archives: map[int64]string{1: "x"}}); len(got) != 0 {
		t.Errorf("disabled: %v", got)
	}
}

func TestBelongs(t *testing.T) {
	for name, want := range map[string]bool{
		"cam_7_main": true, "cam_7_sub": true, "cam_7_main_at3": true,
		"cam_70_main": false, "cam_7_main_atx": false, "cam_7_mainx": false, "cam_17_main_at1": false,
	} {
		if belongs(name, 7) != want {
			t.Errorf("%s", name)
		}
	}
}
