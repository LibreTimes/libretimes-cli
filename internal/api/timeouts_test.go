package api

import (
	"testing"
	"time"
)

// LT_TIMEOUT_SECONDS scales every phase together. The failure mode worth
// guarding is a malformed value producing a client that times out instantly —
// that turns a typo into "the API is down".
func TestTimeoutScaleRejectsNonsense(t *testing.T) {
	// NaN and Inf are the sharp ones: ParseFloat accepts both, and NaN fails
	// every ordinary comparison including `<= 0`.
	for _, raw := range []string{"", "abc", "0", "-30", "  ", "NaN", "nan", "Inf", "+Inf", "-Inf"} {
		t.Setenv("LT_TIMEOUT_SECONDS", raw)
		if got := timeoutScale(); got != 1 {
			t.Errorf("LT_TIMEOUT_SECONDS=%q gave scale %v, want 1 (the defaults)", raw, got)
		}
	}
}

func TestTimeoutScaleAppliesToEveryPhase(t *testing.T) {
	// Half the default overall budget should halve every phase.
	t.Setenv("LT_TIMEOUT_SECONDS", "90")
	factor := timeoutScale()
	if factor != 0.5 {
		t.Fatalf("scale = %v, want 0.5", factor)
	}
	for _, tc := range []struct {
		name string
		base time.Duration
		want time.Duration
	}{
		{"connect", defaultConnectTimeout, 7500 * time.Millisecond},
		{"tls", defaultTLSHandshake, 7500 * time.Millisecond},
		{"read", defaultReadTimeout, 30 * time.Second},
		{"overall", defaultOverallTimeout, 90 * time.Second},
	} {
		if got := scaled(tc.base, factor); got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A tiny value must not produce a sub-second budget no healthy request can meet.
func TestScaledHasAFloor(t *testing.T) {
	if got := scaled(defaultConnectTimeout, 0.001); got < time.Second {
		t.Errorf("scaled floor = %v, want >= 1s", got)
	}
}

// The regression these numbers exist for: the old datacenter budget failed a
// connect that really took 3.7s and a response that really took 18.2s.
func TestBudgetsCoverTheObservedWorstCase(t *testing.T) {
	if defaultConnectTimeout <= 3700*time.Millisecond {
		t.Errorf("connect budget %v does not cover the 3.7s observed worst case",
			defaultConnectTimeout)
	}
	if defaultReadTimeout <= 18200*time.Millisecond {
		t.Errorf("read budget %v does not cover the 18.2s observed worst case",
			defaultReadTimeout)
	}
	if defaultOverallTimeout < defaultReadTimeout {
		t.Error("the overall budget must not be shorter than a single phase")
	}
}
