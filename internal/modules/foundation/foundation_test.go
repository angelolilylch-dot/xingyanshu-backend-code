package foundation

import "testing"

func TestNormalizeRegionLevel(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"0":        "NATION",
		"1":        "PROVINCE",
		"2":        "CITY",
		"3":        "DISTRICT",
		"4":        "DISTRICT",
		"province": "PROVINCE",
		" CITY ":   "CITY",
		"county":   "DISTRICT",
	}
	for input, expected := range cases {
		if actual := normalizeRegionLevel(input); actual != expected {
			t.Errorf("normalizeRegionLevel(%q) = %q, want %q", input, actual, expected)
		}
	}
}
