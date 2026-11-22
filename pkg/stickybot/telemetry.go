package stickybot

import "github.com/sadbox/svc/telemetry"

// retryFailedJob is the 5-minute retry of failed loads, saves and file
// deletions (sadbox_job_*{job_name="retry_failed"}); it fails while any is
// outstanding. The 30s heartbeat is botutil's gateway_ready.
var retryFailedJob = telemetry.NewJob("retry_failed")

// modalNames label modal submits (botutil.TraceModals).
var modalNames = []string{"sticky_config"}
