package runner

import "testing"

// The slippage flag is parsed strictly (whole string, base 10, uint32, < 10000); no wrap-around, no trailing characters.
func TestParseBps(t *testing.T) {
	for _, bad := range []string{"4294977295", "50x", "-1", "10000", "65536000", " 5", "5 ", "+5", "0x10", "1e3", "", "99999999999999999999"} {
		if v, err := ParseBps(bad); err == nil {
			t.Errorf("ParseBps(%q) = %d, want an error", bad, v)
		}
	}
	for s, want := range map[string]uint32{"0": 0, "9999": 9999, "50": 50, "00": 0} {
		if v, err := ParseBps(s); err != nil || v != want {
			t.Errorf("ParseBps(%q) = %d, %v; want %d", s, v, err, want)
		}
	}
	if Bps("") != nil {
		t.Error("an empty flag must stay nil (= SDK default 50 bps)")
	}
	if p := Bps("0"); p == nil || *p != 0 {
		t.Error("an explicit 0 must be exactly 0 bps")
	}
}
