package server

import (
	"sync"
	"testing"
	"time"
)

func TestPacketBucketUnlimitedRateAlwaysAllows(t *testing.T) {
	clock := newControllableClock(time.Unix(0, 0))
	bucket := newPacketBucket(0, clock.Now)

	for range 1000 {
		if !bucket.Allow() {
			t.Fatal("a rate of zero must mean unlimited, and a packet was refused")
		}
	}
	clock.Advance(-time.Hour) // even a clock that goes backwards must not matter
	if !bucket.Allow() {
		t.Error("an unlimited bucket refused a packet after the clock moved")
	}
}

func TestPacketBucketBurstIsBoundedByCapacity(t *testing.T) {
	clock := newControllableClock(time.Unix(0, 0))
	bucket := newPacketBucket(10, clock.Now)

	allowed := 0
	for range 100 {
		if bucket.Allow() {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("a fresh bucket let %d packets through, want exactly its 10 packet capacity", allowed)
	}
}

func TestPacketBucketRefillsOverTime(t *testing.T) {
	clock := newControllableClock(time.Unix(0, 0))
	bucket := newPacketBucket(10, clock.Now)

	for range 10 {
		if !bucket.Allow() {
			t.Fatal("the bucket should start full")
		}
	}
	if bucket.Allow() {
		t.Fatal("an empty bucket let a packet through before any time passed")
	}
	clock.Advance(300 * time.Millisecond) // 10 packets/second for 0.3s is 3 tokens
	refilled := 0
	for range 10 {
		if bucket.Allow() {
			refilled++
		}
	}
	if refilled != 3 {
		t.Errorf("%d packets were allowed after 300ms at 10/s, want 3", refilled)
	}
}

func TestPacketBucketNeverAccumulatesBeyondCapacity(t *testing.T) {
	clock := newControllableClock(time.Unix(0, 0))
	bucket := newPacketBucket(10, clock.Now)

	for range 10 {
		bucket.Allow()
	}
	clock.Advance(time.Hour) // a long idle period must not buy an unbounded burst
	allowed := 0
	for range 1000 {
		if bucket.Allow() {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("%d packets were allowed after an hour idle, want the 10 packet capacity", allowed)
	}
}

func TestPacketBucketConcurrentCallsNeverGoNegative(t *testing.T) {
	clock := newControllableClock(time.Unix(0, 0))
	const capacity = 64
	bucket := newPacketBucket(capacity, clock.Now)

	var allowed sync.WaitGroup
	var counter sync.Mutex
	total := 0
	var start sync.WaitGroup
	start.Add(1)
	for range capacity * 4 {
		allowed.Add(1)
		go func() {
			defer allowed.Done()
			start.Wait()
			if bucket.Allow() {
				counter.Lock()
				total++
				counter.Unlock()
			}
		}()
	}
	start.Done()
	allowed.Wait()

	if total != capacity {
		t.Errorf("%d of %d concurrent callers were allowed, want exactly the %d token capacity", total, capacity*4, capacity)
	}
	if tokens := bucket.available(); tokens < 0 {
		t.Errorf("available() = %v, want a non-negative token count", tokens)
	}
}
