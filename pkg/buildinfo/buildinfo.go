// Package buildinfo says which build a program is.
package buildinfo

// Version is the build's short commit SHA: the Dockerfile's VERSION, linked
// in by its `go build -ldflags "-X …/pkg/buildinfo.Version=…"` (and checked
// there: each binary must print it). Every program prints it with -version
// (statever.ParseFlags) and sends it as its telemetry's service.version, so
// two builds' metrics and traces can be told apart. "dev" in a build that
// was given none (go run, the dev images).
var Version = "dev"
