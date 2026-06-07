package consolidate

import "testing"

func TestDefaultConfig_Values(t *testing.T) {
	c := DefaultConfig()
	if c.TauLow != 0.90 {
		t.Errorf("TauLow = %v, want 0.90 (raised to curb over-clustering)", c.TauLow)
	}
	if c.TauHigh != 0.97 {
		t.Errorf("TauHigh = %v, want 0.97", c.TauHigh)
	}
	if c.MaxAbstractSize != 6 {
		t.Errorf("MaxAbstractSize = %v, want 6", c.MaxAbstractSize)
	}
}
