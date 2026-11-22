// Package telemetrytest records what a test's code exports through
// svc/telemetry: spans and metrics, in memory.
package telemetrytest

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Recorder holds the spans and metrics recorded since Install.
type Recorder struct {
	spans  *tracetest.InMemoryExporter
	reader *sdkmetric.ManualReader
}

var (
	once sync.Once
	rec  *Recorder
)

// Install makes in-memory providers the global ones, once per test binary:
// OpenTelemetry's global delegation binds instruments made before the first
// provider is set to that provider for good, so a second one would record
// nothing. Tests sharing it should look only at their own spans and series.
func Install(t testing.TB) *Recorder {
	t.Helper()
	once.Do(func() {
		rec = &Recorder{spans: tracetest.NewInMemoryExporter(), reader: sdkmetric.NewManualReader()}
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(rec.spans)))
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(rec.reader)))
	})
	return rec
}

// Spans returns every span ended so far.
func (r *Recorder) Spans() tracetest.SpanStubs { return r.spans.GetSpans() }

// Point is one data point of a metric: its attributes and, for a sum or a
// gauge, its value; for a histogram, its count.
type Point struct {
	Attrs attribute.Set
	Value float64
}

// Points collects the metric called name.
func (r *Recorder) Points(t testing.TB, name string) []Point {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	var out []Point
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Histogram[float64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{p.Attributes, float64(p.Count)})
				}
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{p.Attributes, float64(p.Value)})
				}
			case metricdata.Sum[float64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{p.Attributes, p.Value})
				}
			case metricdata.Gauge[float64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{p.Attributes, p.Value})
				}
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					out = append(out, Point{p.Attributes, float64(p.Value)})
				}
			default:
				t.Fatalf("metric %s: unhandled data type %T", name, m.Data)
			}
		}
	}
	return out
}

// Get is the value of attribute key in p, "" if absent.
func (p Point) Get(key string) string {
	v, _ := p.Attrs.Value(attribute.Key(key))
	return v.String()
}

// exportDropped are the span attributes svc/telemetry drops at export
// (its redact.go), so a test needn't find request data in them: these
// in-memory spans are what the code recorded, before that redaction.
var exportDropped = []string{
	"exception.message", "exception.stacktrace", "url.full", "url.path", "url.query", "url.fragment",
	"user_agent.original", "client.address", "client.port", "network.peer.address", "network.peer.port",
}

// Exported is what of attrs leaves the process: everything but the
// attributes svc/telemetry drops and its request and response headers.
// (It also cuts URLs inside other strings to scheme and host; no attribute
// checked here holds one.)
func Exported(attrs []attribute.KeyValue) []attribute.KeyValue {
	var out []attribute.KeyValue
	for _, kv := range attrs {
		k := string(kv.Key)
		if slices.Contains(exportDropped, k) || strings.HasPrefix(k, "http.request.header.") || strings.HasPrefix(k, "http.response.header.") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
