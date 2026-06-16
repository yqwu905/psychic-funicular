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

### M1 — 监控 MVP ✅（已完成）
- ✅ 采集器接口 + CPU/内存/磁盘（gopsutil）。
- ✅ GPU 采集（解析 nvidia-smi CSV，免 cgo、静态二进制；NVML 留作可选增强）。
- ✅ 昇腾 NPU 采集（解析 `npu-smi info`，统一到设备抽象，附解析单测）。
- ✅ Agent 注册即上报 + 周期采样；Server 近线指标存储 + `ListMetrics` 查询。
- ✅ Prometheus `/metrics` 端点；`skctl top`/`gpu`/`npu` 展示实时资源。
- ✅ **已演示**：实时看到各节点 CPU/内存/磁盘利用率（有卡环境同样可见 GPU/NPU）。

### M2 — 调度 MVP ✅（已完成）
- ✅ 作业模型与状态机（PENDING→ASSIGNED→RUNNING→COMPLETED/FAILED/CANCELLED/TIMEOUT）。
- ✅ FIFO+优先级调度（纯函数 `Plan`：分区/CPU/内存/设备匹配 + 资源核算，附单测），轮询式下发。
- ✅ 执行器：进程组管理、设备隔离（`CUDA_VISIBLE_DEVICES`/`ASCEND_RT_VISIBLE_DEVICES`）、
  walltime 超时（SIGTERM→SIGKILL）、日志流式捕获、退出码上报。
- ✅ `skctl submit/queue/cancel/logs`。
- ✅ **已演示**：提交→排队→运行→看日志→完成/失败/取消/超时全链路。
- ⏭ cgroup v2 CPU/内存硬限额下沉到 M5 硬化（M2 已实现 env 设备隔离，cgroup 强约束待补）。

### M3 — SSH 传输 ✅（已完成）
- ✅ 传输抽象 + 控制平面发起 SSH + **反向端口转发**（Agent 为 gRPC 客户端，故用 `-R` 而非 `-L`）：
  容器仅需开放 SSH 端口（**端口任意，不限 22**），除 SSH 外无需开放/出站任何端口。
- ✅ 主机公钥校验（known_host）、断线指数退避重连、SSH 保活（含单测）。
- ✅ **已演示**：真实 sshd 跑在 2222，节点经隧道注册并在其中跑作业——注册/指标/调度/执行/日志
  全链路走在一条 SSH 连接里。
- ⏭ Agent 经 SSH 自举（推送二进制 + 远程启动）当前为手动步骤（`scp -P <port>` + 运行），自动化列为后续项。

### M4 — 事件与通知 ✅（已完成）
- ✅ 事件引擎 + 规则路由 + 去重/冷却（含单测）；事件与通知均持久化、可查。
- ✅ 通知器接口 + 注册机制 + 内置 log 默认 sink；真实渠道由使用方实现接口接入（框架不内置）。
- ✅ 服务端检测器：`disk.full`/`disk.recovered`、`device.idle`（区分已分配→占用者 / 空闲→管理员）、
  `job.completed`/`job.failed`、`node.down`。
- ✅ 配置化规则（`${label}` 动态接收人）；`skctl events` / `skctl notifications`。
- ✅ **已演示**：disk.full 触达 admins、job.completed 触达提交者(`${owner}`)。
- ⏭ 用户联系方式/偏好、`skctl notify test`、真实渠道实现列为后续/使用方侧。

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
