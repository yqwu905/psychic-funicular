package server

import (
	"context"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/yqwu905/psychic-funicular/internal/metrics"
	"github.com/yqwu905/psychic-funicular/internal/store"
)

// 指标描述符。节点级带 node 标签；磁盘加 mount；设备加 kind/vendor/index/uuid/name。
var (
	descNodeUp   = prometheus.NewDesc("skipper_node_up", "1 if node state is UP else 0", []string{"node"}, nil)
	descCPUUtil  = prometheus.NewDesc("skipper_node_cpu_utilization", "Node CPU utilization percent (0-100)", []string{"node"}, nil)
	descLoad1    = prometheus.NewDesc("skipper_node_load1", "Node 1-minute load average", []string{"node"}, nil)
	descMemTotal = prometheus.NewDesc("skipper_node_mem_total_bytes", "Node total memory in bytes", []string{"node"}, nil)
	descMemUsed  = prometheus.NewDesc("skipper_node_mem_used_bytes", "Node used memory in bytes", []string{"node"}, nil)

	diskLabels    = []string{"node", "mount"}
	descDiskTotal = prometheus.NewDesc("skipper_disk_total_bytes", "Filesystem total bytes", diskLabels, nil)
	descDiskUsed  = prometheus.NewDesc("skipper_disk_used_bytes", "Filesystem used bytes", diskLabels, nil)
	descDiskPct   = prometheus.NewDesc("skipper_disk_used_percent", "Filesystem used percent (0-100)", diskLabels, nil)

	devLabels    = []string{"node", "kind", "vendor", "index", "uuid", "name"}
	descDevUtil  = prometheus.NewDesc("skipper_device_utilization", "Accelerator utilization percent (0-100)", devLabels, nil)
	descDevMemT  = prometheus.NewDesc("skipper_device_mem_total_bytes", "Accelerator total memory bytes", devLabels, nil)
	descDevMemU  = prometheus.NewDesc("skipper_device_mem_used_bytes", "Accelerator used memory bytes", devLabels, nil)
	descDevTemp  = prometheus.NewDesc("skipper_device_temperature_celsius", "Accelerator temperature in Celsius", devLabels, nil)
	descDevPower = prometheus.NewDesc("skipper_device_power_watts", "Accelerator power draw in watts", devLabels, nil)
)

// promCollector 在抓取时即时聚合节点库存与最新指标快照。
type promCollector struct {
	store   store.Store
	metrics *metrics.Store
}

func (c *promCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		descNodeUp, descCPUUtil, descLoad1, descMemTotal, descMemUsed,
		descDiskTotal, descDiskUsed, descDiskPct,
		descDevUtil, descDevMemT, descDevMemU, descDevTemp, descDevPower,
	} {
		ch <- d
	}
}

func (c *promCollector) Collect(ch chan<- prometheus.Metric) {
	nodes, err := c.store.ListNodes(context.Background())
	if err != nil {
		return
	}
	for _, n := range nodes {
		up := 0.0
		if n.State == store.StateUp {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(descNodeUp, prometheus.GaugeValue, up, n.Name)

		snap, ok := c.metrics.Get(n.ID)
		if !ok || snap == nil {
			continue
		}
		if cpu := snap.GetCpu(); cpu != nil {
			ch <- prometheus.MustNewConstMetric(descCPUUtil, prometheus.GaugeValue, cpu.GetUtilization(), n.Name)
			ch <- prometheus.MustNewConstMetric(descLoad1, prometheus.GaugeValue, cpu.GetLoad1(), n.Name)
		}
		if m := snap.GetMem(); m != nil {
			ch <- prometheus.MustNewConstMetric(descMemTotal, prometheus.GaugeValue, float64(m.GetTotalBytes()), n.Name)
			ch <- prometheus.MustNewConstMetric(descMemUsed, prometheus.GaugeValue, float64(m.GetUsedBytes()), n.Name)
		}
		for _, d := range snap.GetDisks() {
			ch <- prometheus.MustNewConstMetric(descDiskTotal, prometheus.GaugeValue, float64(d.GetTotalBytes()), n.Name, d.GetMount())
			ch <- prometheus.MustNewConstMetric(descDiskUsed, prometheus.GaugeValue, float64(d.GetUsedBytes()), n.Name, d.GetMount())
			ch <- prometheus.MustNewConstMetric(descDiskPct, prometheus.GaugeValue, d.GetUsedPercent(), n.Name, d.GetMount())
		}
		for _, d := range snap.GetDevices() {
			lv := []string{n.Name, d.GetKind(), d.GetVendor(), strconv.FormatUint(uint64(d.GetIndex()), 10), d.GetUuid(), d.GetName()}
			ch <- prometheus.MustNewConstMetric(descDevUtil, prometheus.GaugeValue, d.GetUtilization(), lv...)
			ch <- prometheus.MustNewConstMetric(descDevMemT, prometheus.GaugeValue, float64(d.GetMemTotalBytes()), lv...)
			ch <- prometheus.MustNewConstMetric(descDevMemU, prometheus.GaugeValue, float64(d.GetMemUsedBytes()), lv...)
			ch <- prometheus.MustNewConstMetric(descDevTemp, prometheus.GaugeValue, d.GetTemperatureC(), lv...)
			ch <- prometheus.MustNewConstMetric(descDevPower, prometheus.GaugeValue, d.GetPowerWatts(), lv...)
		}
	}
}
