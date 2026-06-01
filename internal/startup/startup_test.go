package startup

import (
	"context"
	"errors"
	"io"
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

func TestRetryDependencyDefaultsNonPositiveInterval(t *testing.T) {
	attempts := 0
	err := RetryDependency(context.Background(), "test", 0, nil, func(context.Context) error {
		attempts++
		return nil
	})
	if err != nil {
		t.Fatalf("RetryDependency returned error: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestRetryDependencyLogsReadinessAfterRetry(t *testing.T) {
	attempts := 0
	logs := []string{}
	err := RetryDependency(context.Background(), "postgres", time.Millisecond, func(format string, args ...any) {
		logs = append(logs, format)
	}, func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("not ready")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RetryDependency returned error: %v", err)
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "not ready") || !strings.Contains(logs[1], "ready") {
		t.Fatalf("unexpected retry logs: %v", logs)
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
	if !strings.Contains(err.Error(), "test not ready before timeout") {
		t.Fatalf("RetryDependency error = %q, want timeout context", err)
	}
}

func TestPingAnyTCPSucceedsWhenAnyAddressIsReachable(t *testing.T) {
	listener := listenTCP(t)
	defer listener.Close()
	accepted := acceptAndClose(t, listener)

	err := PingAnyTCP(context.Background(), []string{"127.0.0.1:1", listener.Addr().String()})
	if err != nil {
		t.Fatalf("PingAnyTCP returned error: %v", err)
	}
	<-accepted
}

func TestPingAnyTCPRejectsEmptyAddressList(t *testing.T) {
	err := PingAnyTCP(context.Background(), []string{"", "  ", ","})
	if err == nil {
		t.Fatal("PingAnyTCP returned nil, want configured-address error")
	}
	if !strings.Contains(err.Error(), "no addresses configured") {
		t.Fatalf("PingAnyTCP error = %q, want no addresses configured", err)
	}
}

func TestPingTCPReturnsDialErrorForInvalidAddress(t *testing.T) {
	if err := pingTCP(context.Background(), "bad-address"); err == nil {
		t.Fatal("expected invalid address dial error")
	}
}

func TestPingAnyTCPReturnsJoinedUnreachableAddressErrors(t *testing.T) {
	first := closedTCPAddress(t)
	second := closedTCPAddress(t)

	err := PingAnyTCP(context.Background(), []string{first, second})
	if err == nil {
		t.Fatal("PingAnyTCP returned nil, want unreachable-address error")
	}

	message := err.Error()
	for _, address := range []string{first, second} {
		if !strings.Contains(message, address) {
			t.Fatalf("PingAnyTCP error = %q, want failed address %q", message, address)
		}
	}
}

func TestPingAnyTCPClosesConnectionCleanly(t *testing.T) {
	listener := listenTCP(t)
	defer listener.Close()
	closed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		buffer := []byte{0}
		_, err = conn.Read(buffer)
		if errors.Is(err, io.EOF) {
			closed <- nil
			return
		}
		closed <- err
	}()

	if err := PingAnyTCP(context.Background(), []string{listener.Addr().String()}); err != nil {
		t.Fatalf("PingAnyTCP returned error: %v", err)
	}

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("accepted connection did not close cleanly: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for accepted connection to close")
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

func listenTCP(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("tcp listen is not permitted in this sandbox: %v", err)
		}
		t.Fatalf("listen: %v", err)
	}
	return listener
}

func closedTCPAddress(t *testing.T) string {
	t.Helper()
	listener := listenTCP(t)
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return address
}

func acceptAndClose(t *testing.T, listener net.Listener) <-chan struct{} {
	t.Helper()
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	return accepted
}
