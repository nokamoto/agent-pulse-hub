package main

import "testing"

func TestRuntimeByteBudgetDecimalValidation(t *testing.T) {
	for _, test := range []struct {
		input string
		want  uint64
	}{
		{"1", 1},
		{"0001", 1},
		{"67108864", 67108864},
		{"18446744073709551615", ^uint64(0)},
		{"", 0},
		{"0", 0},
		{"000", 0},
		{"+1", 0},
		{"-1", 0},
		{"1.5", 0},
		{"1e3", 0},
		{" 1", 0},
		{"1 ", 0},
		{"18446744073709551616", 0},
	} {
		n, err := parseRuntimeByteBudget(test.input)
		if test.want == 0 {
			if err == nil {
				t.Errorf("invalid %q accepted", test.input)
			}
		} else if err != nil || n != test.want {
			t.Errorf("parse %q = %d,%v", test.input, n, err)
		}
	}
}
