# prefix-route-replay

本地路由事件回放器（仅 Go 标准库）。维护多个邻居公布的 IPv4 前缀，
解释当前最优路径与地址查询结果，不收发真实 BGP 报文。

## 用法

```
go run . -config config.json -events events.jsonl [-save saved.jsonl]
go test ./...
```

- `-config`：JSON 配置，`local_asn` 为正的 32 位整数，`neighbors` 为邻居标识列表。
- `-events`：JSONL 事件文件（缺省读标准输入）。每个事件输出一行 JSON 结果。
- `-save`：把事件流原样保存到文件；从空状态重放该文件结果一致。

## 事件格式

```
{"type":"announce","neighbor":"n1","prefix":"10.1.2.3/8","local_pref":100,"as_path":[65001],"internal_cost":10}
{"type":"withdraw","neighbor":"n1","prefix":"10.0.0.0/8"}
{"type":"neighbor_down","neighbor":"n1"}
{"type":"neighbor_up","neighbor":"n1"}
{"type":"query","address":"10.9.9.9"}
{"type":"export"}
```

- 前缀一律清除主机位（`10.1.2.3/8` 规范化为 `10.0.0.0/8`）。
- 同一邻居对同一规范前缀的新发布替换旧路径。
- 包含本地 ASN 的路径、未知或离线邻居的发布被拒绝；错误事件不覆盖已有候选。
- 撤回只移除指定邻居的候选；不存在的撤回返回 `not found`，其他状态不变。
- 邻居下线撤销其全部候选并重新选路；上线不自动恢复旧路径。

## 选路子集

仅实现以下 BGP 决策子集，按顺序比较：

1. 本地优先级 `local_pref` 较高者；
2. AS 路径较短者（重复 ASN 仍计入长度）；
3. 内部代价 `internal_cost` 较低者；
4. 邻居标识字典序较小者。

不实现 MED、ORIGIN、eBGP/iBGP 等其他 BGP 决策属性。

地址查询先在已有最优前缀中选最长匹配，再返回该前缀的胜出路径；
不同长度的前缀之间不直接比较优先级。

## 输出

每个事件输出一行 JSON：`ok`、`change`（`installed`/`updated`/`best_changed`/
`unchanged`/`removed`）、`reason`（变化原因）、`winner` 或 `route`。
`neighbor_down` 附带按前缀排序的 `affected` 列表；`export` 输出按
前缀地址再按长度排序的固定顺序路由表。
