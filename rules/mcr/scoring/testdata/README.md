# 牌例说明

`fan_cases.json` 为 OpenMajiang 编写的 81 个目标番种覆盖牌例。每行对象的 `fan_id` 是 WMO 2014 编号，`notation` 供人审阅，`input` 是实际 Go API 的输入。方括号为已声明面子；无供牌编号的四张同牌为暗杠，其余默认明副露；序数牌使用 m/p/s，字牌 z=东南西北中发白。末张立牌是和牌张，未放入 `input.hand`。

`upstream_hands.json` 从固定提交的 `vendor/mahjong-algorithm/unit_test.cpp` 中实际 `test_points` 调用提取，保留 `source_line`。花色与字牌编码已经转换为本项目协议格式；上游供牌数字 5–7 表示加杠，算番时归一到对应明杠方向 1–3。天地人和扩展输入不导入。191 个条目的来源按上游 MIT 许可证使用，见上一级 `NOTICE.md`。

历史牌例用于分解、等待形状和实现稳定性回归；总分的独立要求在 `scoring_test.go` 的 WMO 具体断言中。修改某个测试结果应说明原规则或规则版本变化，不能直接用程序的新结果刷新期望。
