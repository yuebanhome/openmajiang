package scoring

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// These are explicit structural/context/exclusion expectations. Every negative
// is still a valid winning shape; rejection of a malformed hand cannot satisfy
// this coverage check. They are authored regressions, not official certification.
func TestAll81FanNegativeCases(t *testing.T) {
	data, err := os.ReadFile("testdata/fan_negative_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID     int    `json:"not_fan_id"`
		Source int    `json:"source_positive_fan_id"`
		Reason string `json:"reason"`
		Input  Input  `json:"input"`
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 81 {
		t.Fatalf("want 81 targeted negative cases, got %d", len(cases))
	}
	seen := map[int]bool{}
	for _, c := range cases {
		if c.ID < 1 || c.ID > 81 || seen[c.ID] || c.Reason == "" || c.Source < 1 || c.Source > 81 {
			t.Fatalf("invalid negative metadata: %+v", c)
		}
		seen[c.ID] = true
		t.Run(fmt.Sprintf("%02d", c.ID), func(t *testing.T) {
			result, err := Evaluate(c.Input)
			if err != nil {
				t.Fatalf("negative must remain structurally winning: %s: %v", c.Reason, err)
			}
			checkSum(t, result)
			if fanCount(result, c.ID) != 0 {
				t.Fatalf("incorrect fan %d: %s: %+v", c.ID, c.Reason, result)
			}
		})
	}
}
