package collector

import "testing"

func TestParseNpuSmi(t *testing.T) {
	const sample = `+--------------------------------------------------------------------------------------+
| npu-smi 23.0.rc1                  Version: 23.0.rc1                                   |
+-------------------------------+-----------------+--------------------------------------+
| NPU     Name                  | Health          | Power(W)    Temp(C)    Hugepages    |
| Chip    Device                | Bus-Id          | AICore(%)   Memory-Usage(MB)        |
+===============================+=================+======================================+
| 0       910B                  | OK              | 88.6        38         0    / 0      |
| 0       0                     | 0000:C1:00.0    | 0           3360 / 15039             |
+===============================+=================+======================================+
| 1       910B                  | OK              | 90.7        40         0    / 0      |
| 0       1                     | 0000:81:00.0    | 12          3361 / 15039             |
+===============================+=================+======================================+
`
	devs := parseNpuSmi(sample)
	if len(devs) != 2 {
		t.Fatalf("want 2 devices, got %d", len(devs))
	}

	d0 := devs[0]
	if d0.Kind != "npu" || d0.Vendor != "ascend" {
		t.Fatalf("unexpected kind/vendor: %s/%s", d0.Kind, d0.Vendor)
	}
	if d0.Index != 0 || d0.Name != "910B" {
		t.Fatalf("unexpected index/name: %d/%s", d0.Index, d0.Name)
	}
	if d0.PowerWatts != 88.6 || d0.TemperatureC != 38 || d0.Utilization != 0 {
		t.Fatalf("unexpected power/temp/util: %v/%v/%v", d0.PowerWatts, d0.TemperatureC, d0.Utilization)
	}
	if d0.MemUsedBytes != 3360*1024*1024 || d0.MemTotalBytes != 15039*1024*1024 {
		t.Fatalf("unexpected mem: used=%d total=%d", d0.MemUsedBytes, d0.MemTotalBytes)
	}

	d1 := devs[1]
	if d1.Index != 1 || d1.Utilization != 12 || d1.PowerWatts != 90.7 {
		t.Fatalf("unexpected dev1: index=%d util=%v power=%v", d1.Index, d1.Utilization, d1.PowerWatts)
	}
}
