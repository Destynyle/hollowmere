package limit

import (
	"testing"
	"time"
)

func TestBucket(t *testing.T) {
	b := New(1, 3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !b.Allow(now) {
			t.Fatalf("burst refused at %d", i)
		}
	}
	if b.Allow(now) {
		t.Fatal("empty bucket allowed")
	}
	if !b.Allow(now.Add(1100 * time.Millisecond)) {
		t.Fatal("bucket did not refill")
	}
	if b.Allow(now.Add(1200 * time.Millisecond)) {
		t.Fatal("refilled too fast")
	}
}
