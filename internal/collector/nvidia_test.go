package collector

import "testing"

func TestParseNvidiaCSV(t *testing.T) {
	const sample = `0, GPU-1a2b3c, NVIDIA A100-SXM4-40GB, 37, 40960, 1234, 45, 123.45
1, GPU-4d5e6f, NVIDIA A100-SXM4-40GB, 0, 40960, 0, 30, [N/A]
`
	devs := parseNvidiaCSV(sample)
	if len(devs) != 2 {
		t.Fatalf("want 2 devices, got %d", len(devs))
	}

	d0 := devs[0]
	if d0.Kind != "gpu" || d0.Vendor != "nvidia" {
		t.Fatalf("unexpected kind/vendor: %s/%s", d0.Kind, d0.Vendor)
	}
	if d0.Index != 0 || d0.Uuid != "GPU-1a2b3c" {
		t.Fatalf("unexpected index/uuid: %d/%s", d0.Index, d0.Uuid)
	}
	if d0.Utilization != 37 || d0.TemperatureC != 45 || d0.PowerWatts != 123.45 {
		t.Fatalf("unexpected util/temp/power: %v/%v/%v", d0.Utilization, d0.TemperatureC, d0.PowerWatts)
	}
	if d0.MemTotalBytes != 40960*1024*1024 || d0.MemUsedBytes != 1234*1024*1024 {
		t.Fatalf("unexpected mem: total=%d used=%d", d0.MemTotalBytes, d0.MemUsedBytes)
	}

	// [N/A] 功耗应记为 0。
	if devs[1].PowerWatts != 0 {
		t.Fatalf("want power 0 for [N/A], got %v", devs[1].PowerWatts)
	}
}
