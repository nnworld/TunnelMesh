package protocol

import (
	"math"
	"testing"
)

func TestShouldFlushWindowUpdate(t *testing.T) {
	cases := []struct {
		name      string
		remaining uint32
		unacked   uint32
		threshold uint32
		want      bool
	}{
		{"accumulated credit", 200 * 1024, DefaultWindowUpdateThreshold, DefaultWindowUpdateThreshold, true},
		{"below threshold", 200 * 1024, DefaultWindowUpdateThreshold - 1, DefaultWindowUpdateThreshold, false},
		{"remaining below one frame", MaxStreamFrame - 1, 1, DefaultWindowUpdateThreshold, true},
		{"no credit to return", 0, 0, DefaultWindowUpdateThreshold, false},
		{"threshold equals window", 0, DefaultWindowUpdateThreshold, DefaultWindowUpdateThreshold, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ShouldFlushWindowUpdate(testCase.remaining, testCase.unacked, testCase.threshold); got != testCase.want {
				t.Fatalf("ShouldFlushWindowUpdate(%d, %d, %d)=%v, want %v", testCase.remaining, testCase.unacked, testCase.threshold, got, testCase.want)
			}
		})
	}
}

func TestNegotiateReceiveWindow(t *testing.T) {
	cases := []struct {
		name       string
		advertised uint32
		want       uint32
	}{
		{"zero means unset", 0, DefaultServerReceiveWindow},
		{"too small to refill", 4096, DefaultServerReceiveWindow},
		{"one byte below the floor", DefaultWindowUpdateThreshold + MaxStreamFrame - 1, DefaultServerReceiveWindow},
		{"exact floor is kept", DefaultWindowUpdateThreshold + MaxStreamFrame, DefaultWindowUpdateThreshold + MaxStreamFrame},
		{"default is kept", DefaultServerReceiveWindow, DefaultServerReceiveWindow},
		{"oversized is capped", math.MaxUint32, DefaultServerReceiveWindow},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := NegotiateReceiveWindow(testCase.advertised)
			if got != testCase.want {
				t.Fatalf("NegotiateReceiveWindow(%d)=%d, want %d", testCase.advertised, got, testCase.want)
			}
			// Whatever window survives must still leave room for a whole frame
			// after the largest possible unacknowledged residue.
			if got < DefaultWindowUpdateThreshold+MaxStreamFrame {
				t.Fatalf("accepted window %d can strand a sender below one frame", got)
			}
		})
	}
}
