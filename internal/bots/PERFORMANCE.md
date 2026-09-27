# 内置策略性能与等价性记录

2026-09-26，Linux/amd64，AMD EPYC 9V74 80-Core Processor，Go 1.27.1，
`GOMAXPROCS=4`（进程可见 9 个逻辑 CPU）。测量保留正常 GC 和调度开销。
这里测量完整 `Choose("basic_heuristic", view, options)`，不含规则投影、数据库、
WebSocket 或网络，不能替代整个平台容量验收。

## 观测与测量方法

`performance_test.go` 构造 263 个观察：256 个固定种子的正常初始牌局，另加 7 个
有重复牌、多种牌型候选或合法和牌动作的牌例。构造牌例保留完整 144 张实体牌守恒；
全部经真实 MCR `Inspect` 生成合法动作，再由 `Project` 生成本人私有视图。
策略没有接收全量牌局状态或他人手牌。

最初逐观察执行 30 次，筛出较慢的 5 例，连同固定初始观察和合法和牌快速路径，
分别测量 200 次。原始诊断在 `latency_baseline_amd64.json`。
近七对观察的完整选择 p95 为 6.553 ms，平均 6.143 ms；另行测量其 14 次向听调用
平均合计 6.302 ms，JSON 解码平均 9.841 µs。分项单独测量，不能逐项相加；
结果说明该策略的主要开销在候选手牌向听搜索。

优化在一次选择内按弃牌种类复用候选评估：丢弃同种实体牌得到相同的剩余牌集合，
使用相同向听、进张和番种估计。每个合法动作保留自己的 `option.ID` 和原排序规则。
例如近七对的 14 个实体弃牌动作仅有 8 种弃牌，密集单色 B 仅有 6 种。
没有更改策略、可见信息、计分公式或接口字段。

优化后的正式比较使用相同 7 个固定观察，每例优化前后各 1,000 次，并交替执行顺序。
前版本是测试中冻结的原始实现；每次配对还验证选出的 ID 一致。
完整结果在 `latency_comparison_amd64.json`，以下单位均为毫秒。

| 观察 | 原 p95 | 复用后 p95 | 原最大值 | 复用后最大值 |
| --- | ---: | ---: | ---: | ---: |
| initial-000 | 1.974 | 1.708 | 3.736 | 3.447 |
| initial-093 | 5.312 | 4.097 | 43.838 | 8.992 |
| initial-206 | 5.413 | 4.222 | 12.464 | 7.751 |
| dense-single-suit-b | 7.020 | 3.358 | 14.011 | 9.055 |
| near-seven-pairs | 7.779 | 4.966 | 13.319 | 11.678 |
| nine-gates-extra-simple | 6.321 | 4.403 | 10.657 | 7.616 |
| dense-complete-hu-short-circuit | 0.017 | 0.014 | 0.185 | 0.158 |

这些是所测合法观察中的结果，不是全部麻将状态的最坏耗时上界。最大值包含主机调度等
抖动；两次采样的 p95 不应直接混算。测量时平台在持有牌局事务的调度路径中调用
`Choose`，所以这一开销会延长事务占用；数据库批量化与其他平台优化仍需容量复测验证。

## 回归与复现

`TestBasicHeuristicReusePreservesEveryOptionRanking` 对 263 个观察逐一比较优化前后
完整动作排序：每次移除共同选中的动作，再比较剩余选项，直到所有原始合法选项耗尽。
该测试通过，包含相同牌种的不同实体 ID；全仓 race 测试也通过。

```sh
go test ./internal/bots -run TestBasicHeuristicReusePreservesEveryOptionRanking -count=1
BOT_COMPARISON_SAMPLES=1000 \
BOT_COMPARISON_REPORT=/tmp/bot-comparison.json \
GOMAXPROCS=4 go test ./internal/bots \
  -run '^TestBasicHeuristicReuseLatencyComparison$' -count=1 -v -timeout=3m
```

`TestBasicHeuristicLatencyProfile` 是当前实现的诊断工具，需显式设置
`BOT_LATENCY_SAMPLES`；现在运行会测到复用后的实现，不能覆盖或冒充已保留的原始基线。
`BenchmarkBasicHeuristic` 可用于常规 Go benchmark。
