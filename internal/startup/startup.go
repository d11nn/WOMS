package startup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

type Logf func(format string, args ...any)

func RetryDependency(ctx context.Context, name string, interval time.Duration, logf Logf, operation func(context.Context) error) error {
	if interval <= 0 {
		interval = time.Second
	}
	var lastErr error
	for attempt := 1; ; attempt++ {
		if err := operation(ctx); err == nil {
			if attempt > 1 && logf != nil {
				logf("%s ready after %d attempts", name, attempt)
			}
			return nil
		} else {
			lastErr = err
			if logf != nil {
				logf("%s not ready attempt=%d error=%v", name, attempt, err)
			}
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s not ready before timeout: %w", name, lastErr)
		case <-timer.C:
		}
	}
}

func PingAnyTCP(ctx context.Context, addresses []string) error {
	addresses = SplitCSV(strings.Join(addresses, ","))
	if len(addresses) == 0 {
		return errors.New("no addresses configured")
	}
	errs := make([]error, 0, len(addresses))
	for _, address := range addresses {
		if err := pingTCP(ctx, address); err == nil {
			return nil
		} else {
			errs = append(errs, fmt.Errorf("%s: %w", address, err))
		}
	}
	return fmt.Errorf("no configured addresses are reachable: %w", errors.Join(errs...))
}

func SplitCSV(value string) []string {
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return values
}

func pingTCP(ctx context.Context, address string) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}
