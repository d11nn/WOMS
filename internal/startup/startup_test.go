package startup

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRetryDependencyEventuallySucceeds(t *testing.T) {
	attempts := 0
	err := RetryDependency(context.Background(), "test", time.Millisecond, nil, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("not ready")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RetryDependency returned error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestRetryDependencyStopsWhenContextExpires(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	err := RetryDependency(ctx, "test", time.Millisecond, nil, func(context.Context) error {
		return errors.New("not ready")
	})
	if err == nil {
		t.Fatal("RetryDependency returned nil, want timeout error")
	}
}

func TestPingAnyTCPSucceedsWhenAnyAddressIsReachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("tcp listen is not permitted in this sandbox: %v", err)
		}
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()

	err = PingAnyTCP(context.Background(), []string{"127.0.0.1:1", listener.Addr().String()})
	if err != nil {
		t.Fatalf("PingAnyTCP returned error: %v", err)
	}
}

func TestSplitCSVTrimsEmptyValues(t *testing.T) {
	got := SplitCSV(" kafka-0:9092, ,kafka-1:9092 ")
	want := []string{"kafka-0:9092", "kafka-1:9092"}
	if len(got) != len(want) {
		t.Fatalf("SplitCSV length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SplitCSV[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
