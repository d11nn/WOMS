package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/d11nn/woms/internal/auth"
	"github.com/d11nn/woms/internal/domain"
)

type scriptedTokenRedisConn struct {
	*strings.Reader
	writes strings.Builder
	closed bool
}

func newScriptedTokenRedisConn(responses string) *scriptedTokenRedisConn {
	return &scriptedTokenRedisConn{Reader: strings.NewReader(responses)}
}

func (c *scriptedTokenRedisConn) Write(p []byte) (int, error) {
	return c.writes.Write(p)
}

func (c *scriptedTokenRedisConn) Close() error {
	c.closed = true
	return nil
}

func (c *scriptedTokenRedisConn) LocalAddr() net.Addr              { return tokenDummyAddr("local") }
func (c *scriptedTokenRedisConn) RemoteAddr() net.Addr             { return tokenDummyAddr("remote") }
func (c *scriptedTokenRedisConn) SetDeadline(time.Time) error      { return nil }
func (c *scriptedTokenRedisConn) SetReadDeadline(time.Time) error  { return nil }
func (c *scriptedTokenRedisConn) SetWriteDeadline(time.Time) error { return nil }

type tokenDummyAddr string

func (a tokenDummyAddr) Network() string { return string(a) }
func (a tokenDummyAddr) String() string  { return string(a) }

func TestRedisCommandEncodesRESPArrays(t *testing.T) {
	got := string(redisCommand("SET", "woms:auth:token:test", "1", "EX", "60"))
	want := "*5\r\n$3\r\nSET\r\n$20\r\nwoms:auth:token:test\r\n$1\r\n1\r\n$2\r\nEX\r\n$2\r\n60\r\n"
	if got != want {
		t.Fatalf("unexpected RESP command:\nwant %q\ngot  %q", want, got)
	}
}

func TestReadRedisValueMapsNilBulkToMissingSession(t *testing.T) {
	_, err := readRedisValue(bufio.NewReader(strings.NewReader("$-1\r\n")))
	if err != ErrTokenSessionNotFound {
		t.Fatalf("expected ErrTokenSessionNotFound, got %v", err)
	}
}

func TestReadRedisValueParsesSupportedReplies(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple string", input: "+PONG\r\n", want: "PONG"},
		{name: "integer", input: ":42\r\n", want: "42"},
		{name: "bulk", input: "$5\r\nvalue\r\n", want: "value"},
		{name: "empty bulk", input: "$0\r\n\r\n", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readRedisValue(bufio.NewReader(strings.NewReader(tt.input)))
			if err != nil {
				t.Fatalf("readRedisValue returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("unexpected value: want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestReadRedisValueReturnsDeterministicErrors(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantError string
		wantIs    error
	}{
		{name: "protocol error", input: "-ERR invalid password\r\n", wantError: "ERR invalid password"},
		{name: "nil bulk", input: "$-1\r\n", wantIs: ErrTokenSessionNotFound},
		{name: "malformed length", input: "$abc\r\n", wantError: `strconv.Atoi: parsing "abc": invalid syntax`},
		{name: "partial bulk read", input: "$5\r\nva", wantIs: io.ErrUnexpectedEOF},
		{name: "malformed bulk terminator", input: "$5\r\nvaluexx", wantError: "malformed redis bulk string terminator"},
		{name: "unsupported prefix", input: "*1\r\n+OK\r\n", wantError: `unsupported redis response prefix '*'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readRedisValue(bufio.NewReader(strings.NewReader(tt.input)))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantIs != nil {
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("expected error matching %v, got %v", tt.wantIs, err)
				}
				return
			}
			if err.Error() != tt.wantError {
				t.Fatalf("unexpected error: want %q, got %q", tt.wantError, err.Error())
			}
		})
	}
}

func TestRedisTokenSessionStoreIntegrationLifecycle(t *testing.T) {
	store := newRedisTokenSessionIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer store.Close()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping redis: %v", err)
	}
	if !store.TracksSessions() {
		t.Fatal("redis token session store should track sessions")
	}

	token := redisTokenSessionIntegrationToken(t)
	defer deleteRedisTokenSession(t, store, token)
	claims := redisTokenSessionClaims(time.Now().Add(5 * time.Minute))

	if err := store.Save(ctx, token, claims); err != nil {
		t.Fatalf("save token session: %v", err)
	}
	if err := store.Verify(ctx, token, claims); err != nil {
		t.Fatalf("verify token session: %v", err)
	}

	revoked, err := store.Revoke(ctx, token)
	if err != nil {
		t.Fatalf("revoke token session: %v", err)
	}
	if !revoked {
		t.Fatal("expected Revoke to report a deleted session")
	}
	if err := store.Verify(ctx, token, claims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Fatalf("expected revoked token to be missing, got %v", err)
	}

	revoked, err = store.Revoke(ctx, token)
	if err != nil {
		t.Fatalf("revoke missing token session: %v", err)
	}
	if revoked {
		t.Fatal("expected Revoke to report no deleted session after prior revoke")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close token session store: %v", err)
	}
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping redis after close: %v", err)
	}
}

func TestRedisTokenSessionStoreIntegrationRejectsEmptyAndExpiredTokens(t *testing.T) {
	store := newRedisTokenSessionIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer store.Close()

	validClaims := redisTokenSessionClaims(time.Now().Add(time.Minute))
	if err := store.Save(ctx, "", validClaims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Fatalf("expected empty token save to fail as missing session, got %v", err)
	}
	if err := store.Verify(ctx, "", validClaims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Fatalf("expected empty token verify to fail as missing session, got %v", err)
	}

	expiredToken := redisTokenSessionIntegrationToken(t)
	if err := store.Save(ctx, expiredToken, redisTokenSessionClaims(time.Now().Add(-time.Second))); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Fatalf("expected expired token save to fail as missing session, got %v", err)
	}
}

func TestRedisTokenSessionStoreIntegrationExpiresSavedToken(t *testing.T) {
	store := newRedisTokenSessionIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	defer store.Close()

	token := redisTokenSessionIntegrationToken(t)
	defer deleteRedisTokenSession(t, store, token)
	claims := redisTokenSessionClaims(time.Now().Add(1500 * time.Millisecond))

	if err := store.Save(ctx, token, claims); err != nil {
		t.Fatalf("save expiring token session: %v", err)
	}
	if err := store.Verify(ctx, token, claims); err != nil {
		t.Fatalf("verify expiring token session before ttl: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		err := store.Verify(ctx, token, claims)
		if errors.Is(err, ErrTokenSessionNotFound) {
			return
		}
		if err != nil {
			t.Fatalf("verify expiring token session: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("token session did not expire before deadline")
}

func TestRedisTokenSessionStoreIntegrationReconnectsAfterCommandError(t *testing.T) {
	store := newRedisTokenSessionIntegrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer store.Close()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("initial ping redis: %v", err)
	}
	if _, err := store.command(ctx, "WOMS_UNKNOWN_COMMAND"); err == nil {
		t.Fatal("expected invalid Redis command to fail")
	}
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping redis after command error: %v", err)
	}
}

func newRedisTokenSessionIntegrationStore(t *testing.T) *RedisTokenSessionStore {
	t.Helper()
	addr := redisTokenSessionIntegrationAddr(t)
	store := NewRedisTokenSessionStore(addr)
	store.timeout = 500 * time.Millisecond
	return store
}

func redisTokenSessionIntegrationAddr(t *testing.T) string {
	t.Helper()
	requireRedisTokenSessionIntegration(t)
	addr := strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	if addr == "" {
		t.Skip("REDIS_ADDR is not set; skipping manual Redis token session integration test")
	}
	return addr
}

func requireRedisTokenSessionIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("WOMS_INTEGRATION_TESTS") != "1" {
		t.Skip("WOMS_INTEGRATION_TESTS=1 is not set; skipping manual Redis token session integration test")
	}
}

func redisTokenSessionIntegrationToken(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer("/", ":", " ", "_").Replace(t.Name())
	return fmt.Sprintf("woms-test-token-%s-%d", name, time.Now().UnixNano())
}

func redisTokenSessionClaims(expires time.Time) auth.Claims {
	return auth.Claims{
		Subject: "redis-session-test-user",
		Role:    domain.RoleSales,
		Expires: expires.Unix(),
	}
}

func deleteRedisTokenSession(t *testing.T, store *RedisTokenSessionStore, token string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := store.command(ctx, "DEL", tokenSessionKey(token)); err != nil {
		t.Logf("cleanup redis token session %q: %v", token, err)
	}
}

func readRESPCmd(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, errors.New("invalid protocol")
	}
	countStr := strings.TrimSpace(line[1:])
	count, err := strconv.Atoi(countStr)
	if err != nil {
		return nil, err
	}
	var args []string
	for i := 0; i < count; i++ {
		lenLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(lenLine, "$") {
			return nil, errors.New("invalid bulk string prefix")
		}
		length, _ := strconv.Atoi(strings.TrimSpace(lenLine[1:]))
		buf := make([]byte, length+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:length]))
	}
	return args, nil
}

func handleMockRedisConn(c net.Conn, handler func(cmd string, args []string) string) {
	defer c.Close()
	reader := bufio.NewReader(c)
	for {
		args, err := readRESPCmd(reader)
		if err != nil {
			return
		}
		if len(args) == 0 {
			return
		}
		cmd := strings.ToUpper(args[0])
		resp := handler(cmd, args[1:])
		_, _ = c.Write([]byte(resp))
	}
}

func startMockRedisTokenSessionServer(t *testing.T, handler func(cmd string, args []string) string) (string, func()) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("tcp listen is not permitted in this sandbox: %v", err)
		}
		t.Fatalf("failed to start mock redis: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			go handleMockRedisConn(conn, handler)
		}
	}()

	return l.Addr().String(), func() {
		cancel()
		_ = l.Close()
		wg.Wait()
	}
}

func TestMockRedisTokenSessionStore_Noop(t *testing.T) {
	noop := NoopTokenSessionStore{}
	if err := noop.Save(context.Background(), "t", auth.Claims{}); err != nil {
		t.Errorf("noop.Save failed: %v", err)
	}
	if err := noop.Verify(context.Background(), "t", auth.Claims{}); err != nil {
		t.Errorf("noop.Verify failed: %v", err)
	}
	if ok, err := noop.Revoke(context.Background(), "t"); ok || err != nil {
		t.Errorf("noop.Revoke failed: ok=%v, err=%v", ok, err)
	}
	if noop.TracksSessions() {
		t.Error("noop should not track sessions")
	}
	if err := noop.Close(); err != nil {
		t.Errorf("noop.Close failed: %v", err)
	}
}

func TestMockRedisTokenSessionStore_Memory(t *testing.T) {
	mem := NewMemoryTokenSessionStore()
	if !mem.TracksSessions() {
		t.Error("memory store should track sessions")
	}
	defer mem.Close()

	ctx := context.Background()
	claims := auth.Claims{
		Subject: "user",
		Role:    domain.RoleSales,
		Expires: time.Now().Add(5 * time.Minute).Unix(),
	}

	// Save invalid
	if err := mem.Save(ctx, "", claims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Errorf("expected ErrTokenSessionNotFound for empty token, got %v", err)
	}
	expiredClaims := auth.Claims{
		Subject: "user",
		Role:    domain.RoleSales,
		Expires: time.Now().Add(-5 * time.Minute).Unix(),
	}
	if err := mem.Save(ctx, "mytoken", expiredClaims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Errorf("expected ErrTokenSessionNotFound for expired claims, got %v", err)
	}

	// Save valid
	if err := mem.Save(ctx, "mytoken", claims); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	// Verify valid
	if err := mem.Verify(ctx, "mytoken", claims); err != nil {
		t.Fatalf("failed to verify: %v", err)
	}

	// Verify missing
	if err := mem.Verify(ctx, "wrongtoken", claims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Errorf("expected ErrTokenSessionNotFound for missing token, got %v", err)
	}

	// Verify expired entry
	mem.sessions[tokenSessionKey("mytoken")] = time.Now().Add(-1 * time.Second)
	if err := mem.Verify(ctx, "mytoken", claims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Errorf("expected ErrTokenSessionNotFound for expired saved token, got %v", err)
	}

	// Revoke
	_ = mem.Save(ctx, "mytoken", claims)
	revoked, err := mem.Revoke(ctx, "mytoken")
	if err != nil || !revoked {
		t.Errorf("failed to revoke: revoked=%v, err=%v", revoked, err)
	}
	revoked2, err := mem.Revoke(ctx, "mytoken")
	if err != nil || revoked2 {
		t.Errorf("revoking non-existent token: revoked=%v, err=%v", revoked2, err)
	}
}

func TestMockRedisTokenSessionStore_MockRedisSave(t *testing.T) {
	ctx := context.Background()
	claims := auth.Claims{
		Subject: "user",
		Role:    domain.RoleSales,
		Expires: time.Now().Add(5 * time.Minute).Unix(),
	}
	expiredClaims := auth.Claims{
		Subject: "user",
		Role:    domain.RoleSales,
		Expires: time.Now().Add(-5 * time.Minute).Unix(),
	}

	addr, cleanup := startMockRedisTokenSessionServer(t, func(cmd string, args []string) string {
		switch cmd {
		case "PING":
			return "+PONG\r\n"
		case "SET":
			return "+OK\r\n"
		case "GET":
			if len(args) > 0 && args[0] == tokenSessionKey("token") {
				return "$1\r\n1\r\n"
			}
			return "$-1\r\n"
		case "DEL":
			return ":1\r\n"
		default:
			return "-ERR unknown command\r\n"
		}
	})
	defer cleanup()

	rstore := NewRedisTokenSessionStore(addr)
	rstore.timeout = 500 * time.Millisecond
	defer rstore.Close()

	if !rstore.TracksSessions() {
		t.Error("rstore should track sessions")
	}

	if err := rstore.Ping(ctx); err != nil {
		t.Fatalf("redis ping failed: %v", err)
	}

	// Test Save empty token / invalid ttl
	if err := rstore.Save(ctx, "", claims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Errorf("expected ErrTokenSessionNotFound, got %v", err)
	}
	if err := rstore.Save(ctx, "token", expiredClaims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Errorf("expected ErrTokenSessionNotFound, got %v", err)
	}

	// Test Save sub-second TTL (seconds < 1)
	subSecClaims := auth.Claims{
		Subject: "user",
		Role:    domain.RoleSales,
		Expires: time.Now().Unix() + 1,
	}
	if err := rstore.Save(ctx, "token", subSecClaims); err != nil {
		t.Fatalf("redis save sub-second failed: %v", err)
	}

	// Test Save OK
	if err := rstore.Save(ctx, "token", claims); err != nil {
		t.Fatalf("redis save failed: %v", err)
	}
}

func TestMockRedisTokenSessionStore_MockRedisVerify(t *testing.T) {
	ctx := context.Background()
	claims := auth.Claims{
		Subject: "user",
		Role:    domain.RoleSales,
		Expires: time.Now().Add(5 * time.Minute).Unix(),
	}

	addr, cleanup := startMockRedisTokenSessionServer(t, func(cmd string, args []string) string {
		switch cmd {
		case "PING":
			return "+PONG\r\n"
		case "SET":
			return "+OK\r\n"
		case "GET":
			if len(args) > 0 && args[0] == tokenSessionKey("token") {
				return "$1\r\n1\r\n"
			}
			return "$-1\r\n"
		case "DEL":
			return ":1\r\n"
		default:
			return "-ERR unknown command\r\n"
		}
	})
	defer cleanup()

	rstore := NewRedisTokenSessionStore(addr)
	rstore.timeout = 500 * time.Millisecond
	defer rstore.Close()

	// Test Save OK for Verification
	if err := rstore.Save(ctx, "token", claims); err != nil {
		t.Fatalf("redis save failed: %v", err)
	}

	// Test commandLocked with a custom short context deadline
	shortCtx, shortCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	if err := rstore.Verify(shortCtx, "token", claims); err != nil {
		t.Fatalf("redis verify with short deadline failed: %v", err)
	}
	shortCancel()

	// Test Verify OK
	if err := rstore.Verify(ctx, "token", claims); err != nil {
		t.Fatalf("redis verify failed: %v", err)
	}

	// Test Verify ErrTokenSessionNotFound (GET returns $-1)
	if err := rstore.Verify(ctx, "missing-token", claims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Errorf("expected ErrTokenSessionNotFound, got %v", err)
	}

	// Test Revoke
	rev, err := rstore.Revoke(ctx, "token")
	if err != nil || !rev {
		t.Errorf("redis revoke failed: rev=%v, err=%v", rev, err)
	}
}

func TestRedisTokenSessionStoreErrors(t *testing.T) {
	// Address empty error
	storeEmpty := NewRedisTokenSessionStore("")
	if err := storeEmpty.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "REDIS_ADDR") {
		t.Errorf("expected empty address error, got %v", err)
	}

	// Ping returns non-PONG
	addr, cleanup := startMockRedisTokenSessionServer(t, func(cmd string, args []string) string {
		return "+NOT_PONG\r\n"
	})
	defer cleanup()

	storeBadPing := NewRedisTokenSessionStore(addr)
	defer storeBadPing.Close()
	if err := storeBadPing.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "unexpected redis PING response") {
		t.Errorf("expected bad ping error, got %v", err)
	}

	// Verify returns something other than "1"
	addr2, cleanup2 := startMockRedisTokenSessionServer(t, func(cmd string, args []string) string {
		if cmd == "GET" {
			return "$3\r\nBAD\r\n"
		}
		return "+OK\r\n"
	})
	defer cleanup2()

	storeBadVerify := NewRedisTokenSessionStore(addr2)
	defer storeBadVerify.Close()
	if err := storeBadVerify.Verify(context.Background(), "token", auth.Claims{}); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Errorf("expected ErrTokenSessionNotFound, got %v", err)
	}

	// Command returns err when connection fails
	storeBadConn := NewRedisTokenSessionStore("127.0.0.1:9999") // unlikely to have a server there
	storeBadConn.timeout = 50 * time.Millisecond
	if err := storeBadConn.Ping(context.Background()); err == nil {
		t.Error("expected connection error, got nil")
	}
}

func TestRedisTokenSessionStoreScriptedConnectionLifecycle(t *testing.T) {
	conn := newScriptedTokenRedisConn("+PONG\r\n+OK\r\n$1\r\n1\r\n:1\r\n")
	store := NewRedisTokenSessionStore("scripted")
	store.conn = conn
	store.reader = bufio.NewReader(conn)
	ctx := context.Background()
	claims := auth.Claims{Subject: "user", Role: domain.RoleSales, Expires: time.Now().Add(time.Minute).Unix()}

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
	if err := store.Save(ctx, "token", claims); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if err := store.Verify(ctx, "token", claims); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	revoked, err := store.Revoke(ctx, "token")
	if err != nil || !revoked {
		t.Fatalf("Revoke failed: revoked=%v err=%v", revoked, err)
	}
	if !store.TracksSessions() {
		t.Fatal("RedisTokenSessionStore should track sessions")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !conn.closed || store.conn != nil || store.reader != nil {
		t.Fatalf("Close should clear connection, closed=%v conn=%v reader=%v", conn.closed, store.conn, store.reader)
	}
	writes := conn.writes.String()
	for _, want := range []string{"PING", "SET", "GET", "DEL"} {
		if !strings.Contains(writes, want) {
			t.Fatalf("expected command %s in writes %q", want, writes)
		}
	}
}

func TestRedisTokenSessionStoreScriptedConnectionErrorPaths(t *testing.T) {
	ctx := context.Background()
	claims := auth.Claims{Subject: "user", Role: domain.RoleSales, Expires: time.Now().Add(time.Minute).Unix()}

	store := NewRedisTokenSessionStore("scripted")
	store.conn = newScriptedTokenRedisConn("$3\r\nBAD\r\n")
	store.reader = bufio.NewReader(store.conn)
	if err := store.Verify(ctx, "token", claims); !errors.Is(err, ErrTokenSessionNotFound) {
		t.Fatalf("expected non-1 GET to be missing session, got %v", err)
	}

	conn := newScriptedTokenRedisConn("-ERR boom\r\n")
	store = NewRedisTokenSessionStore("scripted")
	store.conn = conn
	store.reader = bufio.NewReader(conn)
	if err := store.Ping(ctx); err == nil || err.Error() != "ERR boom" {
		t.Fatalf("expected redis error response, got %v", err)
	}
	if !conn.closed || store.conn != nil || store.reader != nil {
		t.Fatalf("expected command error to close connection, closed=%v conn=%v reader=%v", conn.closed, store.conn, store.reader)
	}
}
