package rollout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func BenchmarkStableLog(b *testing.B) {
	for _, timestamp := range []bool{false, true} {
		name := "unchanged"
		if timestamp {
			name = "timestamp_only"
		}
		b.Run(name, func(b *testing.B) {
			p := filepath.Join(b.TempDir(), "source")
			os.WriteFile(p, []byte(header+strings.Repeat(usageLine, 10000)), 0600)
			r := New(p)
			defer r.Close()
			r.Update(context.Background())
			base := time.Now()
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				if timestamp {
					stamp := base.Add(time.Duration(i+1) * time.Second)
					os.Chtimes(p, stamp, stamp)
				}
				if _, err := r.Update(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAppendVerifiedPrefix(b *testing.B) {
	p := filepath.Join(b.TempDir(), "source")
	os.WriteFile(p, []byte(header+strings.Repeat(usageLine, 10000)), 0600)
	r := New(p)
	defer r.Close()
	r.Update(context.Background())
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := f.Write([]byte{'\n'}); err != nil {
			b.Fatal(err)
		}
		if _, err := r.Update(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
