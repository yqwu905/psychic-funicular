# 实施路线

按「先能用、再好用、最后强大」分阶段。每个里程碑都可独立交付、可演示。

## 里程碑

### M0 — 骨架与基础设施 ✅（已完成）
- ✅ 仓库结构、`go.mod`、配置加载（YAML+env）、结构化日志、CI。
- ✅ protobuf 接口定义与 buf 代码生成；gRPC server/agent/skctl 脚手架。
- ✅ 存储接口 + SQLite（modernc 纯 Go）实现；建表迁移；单元测试。
- ✅ Agent 直连注册 + 周期心跳（带退避重试）；Server 失联巡检置 DOWN。
- ✅ `skctl nodes` 展示节点；Dockerfile + docker-compose。
- ✅ **已演示**：server 起 → agent 注册 → `skctl nodes` 看到节点（端到端跑通）。

### M1 — 监控 MVP
- 采集器接口 + CPU/内存/磁盘 + NVIDIA GPU（NVML，降级 nvidia-smi）。
- 昇腾 NPU 采集（`npu-smi info`，统一到 `Device` 抽象）。
- Agent 周期采样、批量上报；Server 指标存储与查询。
- Prometheus 端点；`skctl nodes`/`skctl gpu`/`skctl npu` 展示实时资源。
- **可演示**：实时看到各节点 CPU/内存/磁盘/GPU/NPU 利用率。

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
- 通知器接口 + 注册/路由机制；接入内部通知器实现（框架不内置具体渠道）。
- 三类必备事件：`disk.full`、`device.idle`（区分已分配/未分配）、`job.*`。
- 用户联系方式/偏好；`skctl notify test`。
- **可演示**：磁盘超阈值、GPU 空置、任务结束分别触达对应用户。

### M5 — 增强与硬化（按需）
- 昇腾 NPU 调度（`ASCEND_RT_VISIBLE_DEVICES` 分配）；反向隧道模式。
- Backfill 调度、公平份额/计费、抢占。
- RBAC、审计、密钥后端集成。
- *可选（规模增长后再做）*：Web 控制台；PostgreSQL + HA（多 server + leader 选举）。

## 已确认决策（2026-06）

| 决策 | 选择 | 影响 |
| --- | --- | --- |
| 语言 | **Go** | 单二进制 + SSH/gRPC/监控生态 |
| 集群规模 | **个位数节点（实验室）** | **SQLite 单机 server 为主路径**；HA/PG 降级为「按需后期」 |
| 加速卡 | **NVIDIA GPU + 昇腾 NPU** | 采集器先 NVIDIA 后昇腾，均纳入早期里程碑（M1/M2） |
| 交付优先 | **CLI 优先** | 主攻 `skctl`；Web 控制台移出主线，作为可选增强 |

仍按默认推进、可随时调整：
- **作业执行形态**：进程 + cgroup v2 隔离；嵌套容器作为可选强隔离。
- **通知器**：框架只提供接口与路由/去重/重试；具体通知器由使用方实现接入（不内置渠道）。
