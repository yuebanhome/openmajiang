package scoring

// These are selected explicit exclusions from the pinned source, plus the
// named kong adjudication. Unlisted relationships are not inferred or invented.
// An excluded ID appears only when absent from the chosen final fan table.
var documentedExclusions = map[int][]int{
	1:  {38, 49, 60, 61, 73},
	2:  {54, 59},
	4:  {22, 62},
	5:  {17, 57, 74, 79},
	6:  {19, 22, 62, 79},
	7:  {52, 62, 79},
	8:  {18, 49, 55, 73, 76},
	9:  {38, 73},
	10: {54, 59},
	11: {18, 49, 55, 73},
	12: {49, 62},
	13: {19, 22, 63, 69, 72, 76},
	14: {23, 24, 64, 69},
	15: {23, 24, 49},
	16: {30, 71, 72},
	17: {57, 74},
	18: {49, 55, 73},
	19: {62, 79},
	20: {34, 52, 62},
	21: {49, 68, 76},
	22: {75, 76},
	23: {24, 69},
	24: {23},
	25: {36, 76},
	26: {68, 76},
	27: {37, 76},
	29: {63, 70, 72, 76},
	31: {68},
	34: {52, 62},
	36: {76},
	37: {76},
	40: {75},
	44: {80},
	46: {80},
	47: {58},
	48: {66, 67},
	50: {75},
	53: {79},
	54: {59},
	56: {62, 80},
	57: {74},
	59: {73},
	60: {73},
	61: {73},
	63: {76},
	68: {76},
}

func explain(fans []Fan) []Explanation {
	present := map[int]bool{}
	for _, fan := range fans {
		present[fan.ID] = true
	}
	out := make([]Explanation, 0, len(fans))
	for _, fan := range fans {
		e := Explanation{FanID: fan.ID, Source: "selected_native_fan_table", Reason: "此番来自裁判上下文与所选最高番和型的计算结果。"}
		for _, id := range documentedExclusions[fan.ID] {
			if !present[id] {
				e.ExcludedFanIDs = append(e.ExcludedFanIDs, id)
			}
		}
		if len(e.ExcludedFanIDs) > 0 {
			e.Source = "wmo-2014-77220856-explicit-exclusions"
			e.Reason = "依所选规则的不重复及不计条款，这些附属番不另计；本列表仅列已核对的关系。"
		}
		if (fan.ID == 48 || fan.ID == 67) && (present[5] || present[17]) {
			e.Source = "om-mcr-1-kong-additions"
			e.Reason = "三杠或四杠的暗杠加计使用具名线上裁决：暗杠类只取一个最高项，三暗刻、四暗刻另按真实组成计算。"
		}
		if fan.ID == 81 {
			e.Source = "authoritative_flower_count"
			e.Reason = "仅计已补出的花；花分不计入八分起和门槛。"
		}
		out = append(out, e)
	}
	return out
}
