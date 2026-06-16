# 通知子系统

事件驱动 + 规则路由 + 可插拔通知器。核心要求：在**硬盘满**、**GPU/NPU 长时间空置**、
**任务结束**等事件发生时，通知**指定用户**，且通知器可自定义扩展。

## 1. 事件模型

```go
type Event struct {
    ID       string
    Type     EventType         // 见下表
    Severity Severity          // info | warning | critical
    Source   Source            // node / job / device / system
    Labels   map[string]string // 用于规则匹配与去重，如 node=xx user=yy mount=/data
    Summary  string
    Detail   map[string]any
    DedupKey string            // 同一逻辑事件的稳定键，用于去重与「恢复」配对
    Time     time.Time
}
```

### 1.1 事件类型与来源

| 事件类型 | 来源 | 触发条件（示例） |
| --- | --- | --- |
| `disk.full` | Agent 探测 | 挂载点使用率 > 阈值（如 90%）或 inode 紧张 |
| `disk.recovered` | Agent 探测 | 使用率回落到阈值以下（与 full 配对的「恢复」事件） |
| `device.idle` | Agent/Server | 设备利用率低于阈值且持续超过时长（见 §3） |
| `job.completed` | Agent→Server | 作业进入终态 COMPLETED |
| `job.failed` | Agent→Server | 作业 FAILED / TIMEOUT |
| `node.down` | Server 探测 | 心跳连续超时，节点判定失联 |
| `queue.backlog` | Server 探测 | 队列积压超过阈值（可选） |
| `scheduler.failure` | Server | 调度/下发异常（可选） |

事件分两类来源：**Agent 就近探测**（磁盘、设备、作业退出，时延低）与
**Server 集中探测**（节点失联、队列积压，需全局视角）。

## 2. 规则路由

规则把「事件」映射到「接收人」和「通道」，并控制频率：

```yaml
rules:
  - name: 磁盘告警-数据盘
    match:
      type: disk.full
      labels: { mount: "/data" }      # 标签条件，支持等值/正则/阈值
      severity: ">=warning"
    notify:
      users: [admins]                 # 角色或具体用户
      channels: [internal]            # 使用方注册的通道名；不指定则用用户偏好通道
    throttle:
      cooldown: 1h                    # 同一 DedupKey 冷却，避免刷屏
      resolve: true                   # 收到 disk.recovered 时补发「已恢复」

  - name: 任务结束通知提交者
    match: { type: [job.completed, job.failed] }
    notify:
      users: ["${job.owner}"]         # 动态接收人：作业提交者
      channels: ["${user.preferred}"] # 用户偏好通道
      mail_type_from_job: true        # 尊重作业的 --mail-type=END,FAIL

  - name: GPU 长时空置-提醒占用者
    match:
      type: device.idle
      labels: { allocated: "true" }   # 已分配但闲置（占着不用）
    notify:
      users: ["${device.job.owner}"]
    throttle: { cooldown: 2h }
```

- **接收人解析**：静态（用户/角色/组）+ 动态模板（`${job.owner}`、`${device.job.owner}`）。
- **通道选择**：规则显式指定，或回落到用户的偏好通道与「免打扰时段」。
- **去重 / 冷却 / 恢复**：基于 `DedupKey`，避免重复刷屏；支持发「已恢复」闭环通知。

## 3. 三类必备事件的设计要点

### 3.1 硬盘满（`disk.full`）

- Agent 周期检查各挂载点 `used%` 与 inode，越过阈值发 `disk.full`。
- 冷却避免每个采样周期都告警；回落后发 `disk.recovered` 形成闭环。
- 标签含 `node`、`mount`、`used_pct`，便于规则按盘符/节点定向路由。

### 3.2 GPU/NPU 长时间空置（`device.idle`）

关键在于**区分两种空置**，通知对象与意图完全不同：

| 子类 | 标签 | 含义 | 通知对象 |
| --- | --- | --- | --- |
| 已分配但闲置 | `allocated=true` | 用户占着卡却长时间 0 利用率（浪费） | 作业提交者（提醒释放） |
| 空闲未分配 | `allocated=false` | 集群有空卡可用 | 管理员/等待用户（可选） |

判定：维护设备利用率的**滑动窗口**，当 `util < 阈值`（如 <5%）且**无运行进程**
持续超过 `空置时长`（如 30min）→ 发 `device.idle`。「已分配」状态由调度器的
Allocation 提供，与采集数据 join。

### 3.3 任务结束（`job.completed` / `job.failed`）

- 作业进入终态时由作业管理器触发，默认通知**提交者**。
- 尊重作业级偏好：类似 Slurm `--mail-type=BEGIN,END,FAIL,TIMEOUT` 与 `--mail-user`，
  提交时即可声明「在什么状态、用什么通道、通知谁」。
- 通知内容含作业 ID、退出码、运行时长、峰值资源、日志末尾片段（可配）。

## 4. 通知器接口（仅预留扩展点）

Skipper **只定义通知器接口与注册/路由机制，不内置任何具体通知器**。
具体渠道实现由使用方提供——内部通知器通常很简单（一次 HTTP POST 即可）。
启动时实现下面的接口并注册，路由层即可按名字调度：

```go
type Notifier interface {
    Name() string        // 通道名，由使用方自定，如 "internal" / "im"
    // 把渲染好的消息投递给一组接收地址；返回每个接收人的投递结果
    Notify(ctx context.Context, msg Message, targets []Target) ([]Delivery, error)
}

// 把自己的实现注册进通道表；规则里用同名引用即可
notify.Register("internal", NewInternalNotifier)
```

职责边界：

- **框架负责**：事件 → 规则 → 接收人 → 通道路由、模板渲染、去重/冷却、重试、投递记录。
- **使用方负责**：实现 `Notify` 把消息发出去（HTTP / IM / 邮件……形式不限）。
  一个最小实现可以只有十几行——这正是把「具体通知器」留给内部的原因。

> 设计上不预设任何渠道清单（飞书/钉钉/邮件等都不内置），避免框架背上一堆 SDK 依赖；
> 需要哪个渠道就在使用方侧实现哪个。

### 4.1 消息模板

- 每种事件 × 每种通道可配 Go template（纯文本 / HTML / IM 卡片皆可），由框架渲染后交给通知器。
- 模板变量来自事件 `Labels`/`Detail` 与关联对象（作业、节点、设备、用户）。

## 5. 用户与订阅

```
User ── contacts ──► { email: a@x.com, im: u_xxx, ... }        # 各通道地址(键=通道名)
User ── preferences ─► { preferred: [internal, email], quiet_hours: 22:00-08:00 }
User ── subscriptions ► 关心哪些事件类型/哪些节点组（可选精细化）
```

路由时：事件 → 命中规则 → 解析接收人 → 取各接收人通道地址与偏好 → 通知调度器分发。

## 6. 通知调度器（投递保障）

```
事件引擎(匹配/去重/冷却) → 通知任务入队 → 通知调度器
   → 按通道并发投递 → 失败指数退避重试 → 记录 Delivery(成功/失败/已读回执)
```

- **可靠投递**：入库 + 重试，避免丢通知；记录投递状态便于审计与排障。
- **限流**：对单通道/单用户限速，避免告警风暴。
- **测试**：提供 `skctl notify test --channel <name> --to ...` 自检通道连通性。

## 7. 端到端示例：GPU 占用浪费提醒

```
Agent 采样: gpu0 util=0%, 已分配给 job#42(owner=alice), 持续 35min
  → 满足 util<5% 且 无进程 且 >30min → 发 device.idle{allocated=true, owner=alice}
  → 规则「GPU 长时空置-提醒占用者」命中
  → 接收人解析 ${device.job.owner}=alice，通道=alice 偏好通道
  → 框架渲染消息「你的 job#42 占用 gpu0 已空置 35 分钟，请确认是否释放」
  → 交给使用方注册的通知器投递
  → 通知调度器投递 → 记录 Delivery，冷却 2h
```
