package botutil

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// WaitForShutdown blocks until SIGINT or SIGTERM is received, then logs the shutdown.
func WaitForShutdown(log *slog.Logger, name string) {
	log.Info(name + " is running. Press Ctrl+C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc
	log.Info("Shutting down.")
}
