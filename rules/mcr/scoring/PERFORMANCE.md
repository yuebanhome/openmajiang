# 算番复杂度与实测

这份记录测量完整 Go `Evaluate` 调用：输入校验、CGO、原生最高番搜索、所选拆分提取及说明构造。它不包括 Go 规则状态 JSON、三家响应、数据库、网络和等待窗口；三家同时可和的独立窗口测量在 `rules/mcr/performance_test.go`。

## 多拆分样本来源

`TestIndependentMaximumStandardDecompositions` 使用与第三方番种算法无关的组合枚举器：九个数牌种，每种 0–4 张，普通型先指定将，再按固定面子顺序枚举刻/顺，消除面子排列的重复。穷举 **118,800** 个单色十四张计数向量，普通四面子一将最多有 **4 种**不同拆分，达到该上限的计数向量为 **16 个**。

还穷举各组成部分的牌数，含将的 2/5/8/11/14 张最大拆分数为 1/1/2/3/4，不含将的 0/3/6/9/12 张为 1/1/1/2/3；将四组面子分配给不同花色与字牌不能超过四种。这个结论针对普通牌组的规范拆分，不把同一拆分中和牌张的归属或番种组合称为另一种牌组拆分，也不声称特殊型或本机某次最大延迟是所有输入的严格时间上界。

对这 16 个最多拆分牌形，遍历每一种可能的和牌张及自摸/点和上下文，共 **148** 个输入。另加入 81 番正例和 191 个上游历史牌例，以覆盖七对与普通型竞争、十三幺、不靠、组合龙及排除关系等分支。所有 420 输入先各预热 100 次，再交错运行各 10,000 次，合计 **4,200,000** 次计时。

## 实测结果

环境：Linux/amd64，AMD EPYC 9V74 80-Core Processor（运行时报告可用逻辑 CPU 9），Go 1.27.1，`GOMAXPROCS=8`，CGO C++ `-std=c++11 -O2`。使用正常 GC、单调用计时，不运行 race 或 ASan 插桩。完整时间戳、逐输入分位数和样本量见 `testdata/scoring_latency_amd64.json`。

| 范围 | 样本数 | 平均 | p95 | 实测最大 |
|---|---:|---:|---:|---:|
| 全部 420 输入 | 4,200,000 | 6.087 µs | 9.224 µs | 37.181 ms |
| 最大普通拆分的 148 输入 | 1,480,000 | 8.021 µs | 10.256 µs | 37.181 ms |

最慢的单输入 p95 为 **12.699 µs**：`66677778888999m`，点和 `6m`，普通型有四种规范拆分。最大值包含共享运行环境调度及 GC 的影响；不能把 p95 当最大值，也不能从该记录推断容器/网络端到端延迟。

另运行 Go benchmark：`BenchmarkEvaluateMultiDecomposition-8`，314,173 次，**7,604 ns/op、2,566 B/op、36 allocs/op**。这是吞吐与分配测量，与逐次 wall-clock 分位数使用不同计时口径。

## 复现

从仓库根目录执行，报告路径需使用绝对路径或相对于算番 package 的路径：

```sh
go test ./rules/mcr/scoring -run '^TestIndependentMaximumStandardDecompositions$' -count=1
SCORING_LATENCY_SAMPLES=10000 SCORING_LATENCY_REPORT=/tmp/scoring-latency.json GOMAXPROCS=8 \
  go test ./rules/mcr/scoring -run '^TestScoringLatencyProfile$' -count=1 -v -timeout=3m
go test ./rules/mcr/scoring -run '^$' -bench '^BenchmarkEvaluateMultiDecomposition$' -benchmem -benchtime=2s -count=1
```

未设置 `SCORING_LATENCY_SAMPLES` 时性能采样测试明确跳过。amd64 的结果不能替代 arm64 目标架构实测；本文件没有宣称 arm64 时延已通过。
