package collector

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
)

// ascendCollector 通过解析 `npu-smi info` 的表格输出采集昇腾 NPU 指标。
//
// npu-smi 的表格按「两行一卡」组织（不同 CANN 版本/卡型列略有差异）：
//
//	| NPU  Name   | Health | Power(W)  Temp(C)  Hugepages-Usage         |
//	| Chip Device | Bus-Id | AICore(%) Memory-Usage(MB)  HBM-Usage(MB) |
//
// 解析为最佳努力：以单元格中是否含 Bus-Id(":") 区分两行，配对后产出一块设备；
// 显存取 Memory-Usage / HBM-Usage 中容量较大者（训练卡真实显存在 HBM 列）。
type ascendCollector struct{ log *slog.Logger }

func (ascendCollector) Name() string { return "npu.ascend" }

func (c ascendCollector) Collect(ctx context.Context, snap *skipperv1.MetricsSnapshot) error {
	out, err := exec.CommandContext(ctx, "npu-smi", "info").Output()
	if err != nil {
		return err
	}
	snap.Devices = append(snap.Devices, parseNpuSmi(string(out))...)
	return nil
}

// npuRowA 是「名称行」的中间结果。
type npuRowA struct {
	index uint32
	name  string
	power float64
	temp  float64
}

// parseNpuSmi 解析 `npu-smi info` 的标准表格输出。
func parseNpuSmi(s string) []*skipperv1.DeviceStats {
	var devices []*skipperv1.DeviceStats
	var pending *npuRowA
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) < 3 {
			continue
		}
		// 跳过表头（含字段名而非数值）。
		if isHeaderCell(cells) {
			continue
		}
		if strings.Contains(cells[1], ":") {
			// 设备行（cells[1] 是 Bus-Id）：取 AICore% 与 Memory-Usage，配对前一名称行。
			if pending == nil {
				continue
			}
			util, used, total := parseDeviceRow(cells[2])
			devices = append(devices, &skipperv1.DeviceStats{
				Kind:          "npu",
				Vendor:        "ascend",
				Index:         pending.index,
				Name:          pending.name,
				Utilization:   util,
				MemUsedBytes:  used * 1024 * 1024,
				MemTotalBytes: total * 1024 * 1024,
				TemperatureC:  pending.temp,
				PowerWatts:    pending.power,
			})
			pending = nil
			continue
		}
		// 名称行：cells[0]= "NPU Name"，cells[2]= "Power Temp ..."。
		nameFields := strings.Fields(cells[0])
		ptFields := strings.Fields(cells[2])
		if len(nameFields) < 2 || len(ptFields) < 2 {
			continue
		}
		pending = &npuRowA{
			index: uint32(parseUint(nameFields[0])),
			name:  nameFields[1],
			power: parseFloat(ptFields[0]),
			temp:  parseFloat(ptFields[1]),
		}
	}
	return devices
}

// splitCells 按 '|' 切分一行表格并去除首尾空单元格与空白。
func splitCells(line string) []string {
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isHeaderCell(cells []string) bool {
	joined := strings.ToLower(strings.Join(cells, " "))
	for _, kw := range []string{"name", "health", "aicore", "bus-id", "process", "npu-smi"} {
		if strings.Contains(joined, kw) {
			return true
		}
	}
	return false
}

// parseDeviceRow 从设备行的指标单元格解析 AICore% 与显存(used/total, MB)。
//
// 单元格随 CANN 版本/卡型有两种布局：
//
//	"0   3360 / 15039"            // 仅 Memory-Usage（推理卡 / 旧版）
//	"0   0 / 0   53202/ 65536"    // Memory-Usage + HBM-Usage（910B 等训练卡）
//
// 第一个数是 AICore%；其后每个 "used / total" 组各代表一类内存。训练卡真实
// 显存在 HBM 列，而 Memory-Usage 常为 0/0；且不同版本 '/' 两侧空格不一致
// （如 "53202/ 65536"）。故先把 '/' 规整成独立 token，再取容量(total)最大的
// 一组作为显存——既能命中 HBM，又能忽略 0/0 的 Memory-Usage，兼容两种布局。
func parseDeviceRow(cell string) (util float64, usedMB, totalMB uint64) {
	fields := strings.Fields(strings.ReplaceAll(cell, "/", " / "))
	if len(fields) == 0 {
		return 0, 0, 0
	}
	util = parseFloat(fields[0])
	for i, f := range fields {
		if f != "/" || i == 0 || i+1 >= len(fields) {
			continue
		}
		used, total := parseUint(fields[i-1]), parseUint(fields[i+1])
		if total >= totalMB {
			usedMB, totalMB = used, total
		}
	}
	return util, usedMB, totalMB
}
