package watchdog

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func Notify(state string) error {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return nil
	}
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + strings.TrimPrefix(socket, "@")
	}
	connection, err := net.DialUnix(
		"unixgram",
		nil,
		&net.UnixAddr{Name: socket, Net: "unixgram"},
	)
	if err != nil {
		return fmt.Errorf("dial systemd notify socket: %w", err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte(state)); err != nil {
		return fmt.Errorf("write systemd notification: %w", err)
	}
	return nil
}

func Start(ctx context.Context) {
	raw := os.Getenv("WATCHDOG_USEC")
	microseconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || microseconds <= 0 {
		return
	}
	interval := time.Duration(microseconds) * time.Microsecond / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = Notify("WATCHDOG=1")
		}
	}
}
