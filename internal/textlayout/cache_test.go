package textlayout

import "testing"

func TestCacheBoundsNativeOwnershipAndKeepsHotLabels(t *testing.T) {
	released := map[int]int{}
	c := NewCache(3, 9, func(id int) { released[id]++ })
	for i, word := range []string{"TPS", "tok", "old"} {
		c.Put(Key{Text: word}, i+1, 3)
	}
	if _, ok := c.Get(Key{Text: "TPS"}); !ok {
		t.Fatal("hot label missing")
	}
	c.Put(Key{Text: "new"}, 4, 3)
	if released[2] != 1 || released[1] != 0 {
		t.Fatal("did not evict least recently used layout", released)
	}
	// Replacing a value releases the old reference; style is part of identity.
	c.Put(Key{Text: "TPS"}, 5, 3)
	c.Put(Key{Text: "TPS", Bold: true}, 6, 3)
	if v, ok := c.Get(Key{Text: "TPS"}); !ok || v != 5 {
		t.Fatal("style collision")
	}
	if c.Put(Key{Text: "oversize"}, 7, 10) || c.Put(Key{Text: "invalid"}, 8, -1) {
		t.Fatal("retained over-budget layout")
	}
	c.Close()
	c.Close()
	for i := 1; i <= 8; i++ {
		if released[i] != 1 {
			t.Fatalf("native reference %d released %d times", i, released[i])
		}
	}
	if c.units != 0 || len(c.items) != 0 {
		t.Fatal("cache retains closed layouts")
	}
}

func TestCacheBudgetUnderLongChangingCountersAndDPIResets(t *testing.T) {
	live, peak := 0, 0
	c := NewCache(128, 16384, func(int) { live-- })
	for generation := 0; generation < 4; generation++ {
		for n := 0; n < 10000; n++ {
			live++
			// Unique strings emulate counters, session switches and long metadata.
			key := Key{Text: string(rune(0x1000 + n))}
			c.Put(key, n, 1+n%1000)
			peak = max(peak, live)
			if c.units > 16384 || live > 128 {
				t.Fatal("native cache grows with session length")
			}
		}
		c.Close()
		if live != 0 {
			t.Fatal("DPI/language reset leaks references")
		}
	}
	if peak == 0 {
		t.Fatal("empty stress fixture")
	}
}

func BenchmarkHotLabelCache(b *testing.B) {
	c := NewCache(128, 16384, func(int) {})
	key := Key{Text: "9,223,372,036,854,775,807"}
	c.Put(key, 1, 25)
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := c.Get(key); !ok {
			b.Fatal("lost label")
		}
	}
}
