package relay

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestGRPCStreamConnExposesDirectionalCloseWrite(t *testing.T) {
	fake := &fakeClientStream{}
	conn := &grpcStreamConn{stream: fake}
	halfCloser, ok := any(conn).(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("grpcStreamConn does not expose CloseWrite")
	}
	if err := halfCloser.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if fake.closeSendCalls != 1 {
		t.Fatalf("CloseSend calls = %d, want 1", fake.closeSendCalls)
	}
}

type fakeClientStream struct{ closeSendCalls int }

func (*fakeClientStream) Header() (metadata.MD, error) { return nil, nil }
func (*fakeClientStream) Trailer() metadata.MD         { return nil }
func (s *fakeClientStream) CloseSend() error           { s.closeSendCalls++; return nil }
func (*fakeClientStream) Context() context.Context     { return context.Background() }
func (*fakeClientStream) SendMsg(any) error            { return nil }
func (*fakeClientStream) RecvMsg(any) error            { return nil }
