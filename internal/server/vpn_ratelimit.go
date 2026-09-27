package server

import (
	"sync"
	"time"
)

// packetBucket is one peer's token bucket.
//
// It is a plain mutex-guarded counter rather than golang.org/x/time/rate for one
// reason: the bucket has to be driven by the gateway's injected clock so a test
// can assert refill arithmetic without sleeping, and rate.Limiter reads
// time.Now directly. A peer that sends a few packets a second takes this lock for
// a handful of nanoseconds, which is not where the packet path spends its time.
type packetBucket struct {
	mu       sync.Mutex
	rate     float64
	capacity float64
	tokens   float64
	last     time.Time
	nowFn    func() time.Time
}

// newPacketBucket builds a bucket that allows rate packets per second.
//
// Zero means unlimited, matching the convention every other ceiling in
// server.vpn follows, so a deployment that never sets packet_rate_per_peer gets
// no artificial limit and pays no arithmetic per packet.
//
// The burst allowance is exactly one second of the rate. That is the smallest
// bound that still lets a normal TCP connection start: a bucket that admitted no
// burst at all would refuse the second segment of a window that arrived in the
// same millisecond as the first, which reads to a user as a broken tunnel rather
// than as a rate limit.
func newPacketBucket(rate int, now func() time.Time) *packetBucket {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	bucket := &packetBucket{nowFn: now}
	if rate > 0 {
		bucket.rate = float64(rate)
		bucket.capacity = float64(rate)
		bucket.tokens = bucket.capacity
	}
	bucket.last = now()
	return bucket
}

// Allow consumes one token and reports whether the packet may be forwarded.
func (b *packetBucket) Allow() bool { return b.AllowN(1) }

// AllowN consumes n tokens at once.
//
// It never lets the count go negative: a request larger than the bucket can hold
// is refused whole rather than partly satisfied, so a caller cannot drive the
// bucket into debt and then be silently allowed a burst later to repay it.
func (b *packetBucket) AllowN(n int) bool {
	if b.rate <= 0 {
		return true
	}
	if n <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	cost := float64(n)
	if b.tokens < cost {
		return false
	}
	b.tokens -= cost
	return true
}

// refillLocked adds the tokens earned since the last call. The caller holds mu.
//
// A clock that goes backwards refills nothing rather than draining the bucket:
// the injected clock in a test may be moved in either direction, and a wall clock
// can be stepped by NTP. Neither is a reason to punish a peer.
func (b *packetBucket) refillLocked() {
	now := b.nowFn()
	elapsed := now.Sub(b.last)
	if elapsed > 0 {
		b.last = now
		b.tokens += b.rate * elapsed.Seconds()
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
	}
}

// available reports the current token count, refilling first. It exists so a test
// can assert the bucket never went negative instead of inferring it from a series
// of Allow results.
func (b *packetBucket) available() float64 {
	if b.rate <= 0 {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	return b.tokens
}
