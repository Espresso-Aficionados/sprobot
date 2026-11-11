package botutil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthzUnhealthyWithoutGateway(t *testing.T) {
	b := &BaseBot{}
	rec := httptest.NewRecorder()
	b.healthHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
