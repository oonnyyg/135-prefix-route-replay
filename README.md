# prefix-route-replay

本地路由事件回放器：仅用 Go 标准库实现，维护多个邻居公布的 IPv4 前缀，
解释当前最优路径与地址查询结果，不收发真实 BGP 报文。

## 用法

```sh
go run . -config config.json -events events.jsonl [-save saved.jsonl]
```

- `-config`：JSON 配置，`local_as` 为正的 32 位整数，`neighbors` 为邻居标识列表（默认在线）。
- `-events`：JSONL 事件文件，缺省读标准输入。
- `-save`：把事件规范化后写入文件；之后用 `-events saved.jsonl` 从空状态重放，结果逐行一致。

每个事件输出一行 JSON 结果：`ok`/`error`、`changes`（受影响前缀的变化原因与新旧最优路径）、
`query`（查询结果）或 `table`（导出路由表，按地址再按掩码长固定排序）。

## 事件类型

- `announce`：`neighbor`、`prefix`、`local_pref`（非负）、`as_path`、`internal_cost`（非负）。
- `withdraw`：`neighbor`、`prefix`；不存在时返回未找到错误，状态不变。
- `neighbor_down` / `neighbor_up`：`neighbor`。下线撤销其全部候选并重新选路；上线不恢复旧路径。
- `query`：`address`，在已有最优前缀中最长前缀匹配。
- `export`：导出当前路由表。

## 选路子集说明

本实现只包含 BGP 决策过程的一个子集，对同一前缀的候选依次比较：

1. 本地优先级 `local_pref` 高者优先；
2. AS 路径短者优先（路径中重复的 AS 号仍计入长度）；
3. 内部代价 `internal_cost` 低者优先；
4. 邻居标识字典序小者优先。

不实现 MED、IGP 度量、eBGP/iBGP 区分、router ID 等其他 BGP 决策属性。
地址查询是独立的最长前缀匹配步骤：先选出包含该地址的最长已有最优前缀，
再返回该前缀的胜出路径，不跨前缀长度比较优先级。

其他规则：

- 前缀统一清除主机位（`Masked`）；同一邻居对同一规范前缀的新发布替换旧路径。
- `as_path` 含本地 AS、未知或离线邻居的发布被拒绝；任何错误事件都不改变已有候选。

## 测试

```sh
go test ./...
```

覆盖重复前缀与替换、主路径撤回回退、邻居故障与恢复、拒绝规则、
最长前缀匹配、保存/重放一致性与导出排序。
