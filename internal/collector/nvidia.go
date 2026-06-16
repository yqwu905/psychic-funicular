package collector

import (
	"context"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
)

// nvidiaCollector 通过解析 nvidia-smi 的 CSV 输出采集 GPU 指标。
// 选用 CSV 查询模式而非 NVML，是为了免 cgo、产出静态二进制，且无构建期依赖。
type nvidiaCollector struct{ log *slog.Logger }

func (nvidiaCollector) Name() string { return "gpu.nvidia" }

const nvidiaQuery = "index,uuid,name,utilization.gpu,memory.total,memory.used,temperature.gpu,power.draw"

func (c nvidiaCollector) Collect(ctx context.Context, snap *skipperv1.MetricsSnapshot) error {
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu="+nvidiaQuery, "--format=csv,noheader,nounits").Output()
	if err != nil {
		return err
	}
	snap.Devices = append(snap.Devices, parseNvidiaCSV(string(out))...)
	return nil
}

// parseNvidiaCSV 解析 `nvidia-smi --query-gpu=... --format=csv,noheader,nounits`。
// 列顺序固定为 nvidiaQuery；内存单位 MiB，功耗 W，未知值(如 [N/A])记为 0。
func parseNvidiaCSV(s string) []*skipperv1.DeviceStats {
	var devices []*skipperv1.DeviceStats
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) < 8 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		devices = append(devices, &skipperv1.DeviceStats{
			Kind:          "gpu",
			Vendor:        "nvidia",
			Index:         uint32(parseUint(f[0])),
			Uuid:          f[1],
			Name:          f[2],
			Utilization:   parseFloat(f[3]),
			MemTotalBytes: mibToBytes(f[4]),
			MemUsedBytes:  mibToBytes(f[5]),
			TemperatureC:  parseFloat(f[6]),
			PowerWatts:    parseFloat(f[7]),
		})
	}
	return devices
}

// --- 解析辅助：容忍空串与 "[N/A]" ---

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func parseUint(s string) uint64 {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func mibToBytes(s string) uint64 {
	return parseUint(s) * 1024 * 1024
}
