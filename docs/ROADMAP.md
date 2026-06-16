# 实施路线

按「先能用、再好用、最后强大」分阶段。每个里程碑都可独立交付、可演示。

## 里程碑

### M0 — 骨架与基础设施
- 仓库结构、`go.mod`、配置加载、日志、CI。
- protobuf 接口定义与代码生成；gRPC server/agent 脚手架。
- 存储接口 + SQLite 实现；基础数据表迁移。
- `skctl` 骨架（能连 server、跑通 `nodes` 空列表）。
- **可演示**：server 起、agent 直连注册、`skctl nodes` 看到一个节点。

### M1 — 监控 MVP
- 采集器接口 + CPU/内存/磁盘 + NVIDIA GPU（NVML，降级 nvidia-smi）。
- Agent 周期采样、批量上报；Server 指标存储与查询。
- Prometheus 端点；`skctl nodes`/`skctl gpu` 展示实时资源。
- **可演示**：实时看到各节点 CPU/内存/磁盘/GPU 利用率。

### M2 — 调度 MVP
- node/partition/job/allocation 模型与状态机。
- FIFO + 优先级调度循环；单节点作业执行。
- 执行器：cgroup v2 限额、`CUDA_VISIBLE_DEVICES`、日志捕获、退出码上报。
- `skctl submit/queue/cancel/logs`。
- **可演示**：提交 GPU 作业、排队、运行、看日志、结束回收。

### M3 — SSH 传输
- 传输抽象；SSH 本地转发（控制平面发起）打通「仅 SSH」容器。
- Agent 经 SSH 自举（推送二进制 + 启动）；主机指纹校验。
- 断线重连、心跳、节点失联判定。
- **可演示**：纳管一个只开放 22 端口的 Docker 容器并在其中跑作业。

### M4 — 事件与通知
- 事件引擎 + 规则路由 + 去重/冷却。
- 通道：email / webhook / feishu / dingtalk（+ `exec` 逃生舱）。
- 三类必备事件：`disk.full`、`device.idle`（区分已分配/未分配）、`job.*`。
- 用户联系方式/偏好；`skctl notify test`。
- **可演示**：磁盘超阈值、GPU 空置、任务结束分别触达对应用户。

### M5 — 增强与硬化
- NPU（昇腾）采集与调度；反向隧道模式。
- Backfill 调度、公平份额/计费、抢占。
- Web 控制台；HA（多 server + PG + leader 选举）。
- RBAC、审计、密钥后端集成。

## 待确认事项（影响选型与优先级）

| 问题 | 影响 | 默认假设 |
| --- | --- | --- |
| 语言选型？ | 全局 | **Go**（单二进制 + SSH/gRPC/监控生态） |
| 集群规模？（个位数 vs 上百节点） | 存储/调度复杂度 | 先 SQLite 单机，预留 PG |
| GPU/NPU 厂商范围？ | 采集器优先级 | NVIDIA 优先，昇腾 NPU 次之 |
| 需要 Web UI 吗？还是 CLI 优先？ | M5 取舍 | CLI 优先，Web 后期 |
| 作业执行形态？（直接进程 vs 嵌套容器） | 执行器/隔离 | 进程 + cgroup，嵌套容器可选 |
| 通知渠道优先级？（飞书/钉钉/企业微信/邮件） | M4 排序 | 飞书 + 邮件优先 |

> 这些问题的答案会细化各里程碑的范围与顺序。建议先就「语言、规模、设备厂商、
> 是否要 Web」四项达成一致，再进入 M0 编码。
