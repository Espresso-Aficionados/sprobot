package botutil

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// WaitForShutdown blocks until SIGINT or SIGTERM is received, or the gateway
// closes for good (see OnGatewayClose). In the latter case it returns the
// gateway error, which the bot returns from Run after saving its state.
func (b *BaseBot) WaitForShutdown(name string) error {
	b.Log.Info(name + " is running. Press Ctrl+C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sc)
	select {
	case <-sc:
		b.Log.Info("Shutting down.")
		return nil
	case err := <-b.gatewayFailed:
		return fmt.Errorf("gateway closed: %w", err)
	}
}
