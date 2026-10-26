package termbg

import "testing"

func TestDarkFromColorFgBg(t *testing.T) {
	cases := []struct {
		in   string
		dark bool
	}{
		{"", true},
		{"15;0", true},
		{"0;15", false},
		{"0;7", false},
		{"0;default;15", false},
		{"7;default;0", true},
		{"garbage", true},
		{"0;default", true},
	}
	for _, c := range cases {
		if got := DarkFromColorFgBg(c.in); got != c.dark {
			t.Errorf("DarkFromColorFgBg(%q) = %v, want %v", c.in, got, c.dark)
		}
	}
}
