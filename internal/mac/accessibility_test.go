package mac

import (
	"context"
	"testing"
	"time"
)

func TestAccessibilityActivationRetriesFailureWithoutDebouncingSuccess(t *testing.T) {
	f := newFakeAX()
	f.enableCode = -25211 // Permission not yet granted.
	var activation axActivation
	now := time.Now()
	activation.ensure(context.Background(), f, 1, now)
	activation.ensure(context.Background(), f, 1, now.Add(time.Second))
	if f.enableCalls != 1 || activation.requested || activation.code != -25211 {
		t.Fatalf("unavailable Accessibility retry was not bounded: %+v", activation)
	}
	f.enableCode = 0
	activation.ensure(context.Background(), f, 1, now.Add(3*time.Second))
	for i := 1; i <= 20; i++ {
		activation.ensure(context.Background(), f, 1, now.Add(3*time.Second+time.Duration(i)*500*time.Millisecond))
	}
	if f.enableCalls != 2 || !activation.requested || activation.code != 0 {
		t.Fatalf("successful request was repeated or not recorded: calls=%d %+v", f.enableCalls, activation)
	}
	// A replacement Desktop process gets its own activation lifetime.
	var replacement axActivation
	replacement.ensure(context.Background(), f, 2, now.Add(time.Minute))
	if f.enableCalls != 3 || !replacement.requested {
		t.Fatal("replacement process did not enable its tree")
	}
}

func TestAccessibilityActivationStopsOnCancellation(t *testing.T) {
	f := newFakeAX()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var activation axActivation
	activation.ensure(ctx, f, 1, time.Now())
	activation.ensure(context.Background(), f, 0, time.Now())
	if f.enableCalls != 0 {
		t.Fatal("activation queried a cancelled or missing application")
	}
}

func TestLocatorRequestsAccessibilityOnceAcrossProbes(t *testing.T) {
	f := newFakeAX()
	v, home := testLocator(t, f)
	for range 3 {
		o := probeForTest(v, home)
		if !o.AXEnableRequested || o.AXEnableCode != 0 {
			t.Fatalf("activation status missing: %+v", o)
		}
	}
	if f.enableCalls != 1 {
		t.Fatalf("probe reset Electron's activation debounce: calls=%d", f.enableCalls)
	}
}
