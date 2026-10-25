//go:build unix

package supervisor

import (
	"context"
	"testing"
	"time"
)

func TestStopSendsSIGTERMToTheWorker(t *testing.T) {
	child, err := (ExecLauncher{}).Start(context.Background(), helperSpec("sigterm"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := child.Stop(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	// Exit code 7 is the helper's SIGTERM handler, so the signal got through
	// rather than the process being killed.
	if res := waitResult(t, child, time.Second); res.Code != 7 {
		t.Fatalf("exit = %+v, want 7 from the SIGTERM handler", res)
	}
}
