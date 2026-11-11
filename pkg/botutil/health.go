package botutil

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/disgoorg/disgo/gateway"
)

// HealthAddr is where a bot serves its container healthcheck: loopback only,
// inside the bot's own container.
const HealthAddr = "127.0.0.1:8099"

// HealthURL is what `<bot> -healthcheck` (healthcheck.Run) asks.
const HealthURL = "http://" + HealthAddr + "/healthz"

// ServeHealth serves GET /healthz on HealthAddr until the process exits. It
// answers 200 while the gateway is connected and ready, the same rule as
// PingHealthcheck, and 503 otherwise. Call it before OpenGateway, so the
// container's healthcheck gets an answer during startup too.
func (b *BaseBot) ServeHealth() {
	srv := &http.Server{
		Addr:              HealthAddr,
		Handler:           b.healthHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			b.Log.Error("Health server stopped", "error", err)
		}
	}()
}

func (b *BaseBot) healthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if status := b.gatewayStatus(); status != gateway.StatusReady {
			http.Error(w, fmt.Sprintf("gateway not ready: %v", status), http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "ok\n")
	})
	return mux
}
