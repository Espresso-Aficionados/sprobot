package botutil

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// fatalCloseDelay is how long a bot whose gateway closed for good waits
// before exiting. Closes such as 4004 or 4014 recur on every restart, and
// each restart identifies again: Discord resets the token of a bot that
// identifies more than 1000 times a day, and a container restarted every
// minute would get there. A variable so tests can shorten it.
var fatalCloseDelay = 10 * time.Minute

// WaitForShutdown blocks until SIGINT or SIGTERM is received, or the gateway
// closes for good (see OnGatewayClose). In the latter case it waits
// fatalCloseDelay, or until a signal, and returns the gateway error, which
// the bot returns from Run after saving its state.
func (b *BaseBot) WaitForShutdown(name string) error {
	b.Log.Info(name + " is running. Press Ctrl+C to exit.")
	// The signals stay caught after this returns, so a second Ctrl+C can't
	// kill the bot before it has saved its state.
	sc := shutdownSignals()
	select {
	case <-sc:
		b.Log.Info("Shutting down.")
		return nil
	case err := <-b.gatewayFailed:
		b.holdBeforeExit(sc)
		return fmt.Errorf("gateway closed: %w", err)
	}
}

// OpenGateway opens the gateway connection. disgo retries everything but a
// close it won't reconnect on, so an error here is one that every restart
// would hit again: it waits fatalCloseDelay, or until a signal, before
// returning it.
func (b *BaseBot) OpenGateway(ctx context.Context) error {
	err := b.Client.OpenGateway(ctx)
	if err != nil && gatewayCloseFatal(err, false) {
		b.Log.Error("Gateway refused the connection", "error", err)
		b.holdBeforeExit(shutdownSignals())
	}
	return err
}

func shutdownSignals() <-chan os.Signal {
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	return sc
}

// holdBeforeExit waits fatalCloseDelay, or until a signal, so a bot that
// can't connect restarts slowly (see fatalCloseDelay).
func (b *BaseBot) holdBeforeExit(sc <-chan os.Signal) {
	b.Log.Error("Gateway is down for good, exiting after a delay", "delay", fatalCloseDelay)
	select {
	case <-sc:
	case <-time.After(fatalCloseDelay):
	}
}
