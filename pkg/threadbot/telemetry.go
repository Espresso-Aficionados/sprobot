package threadbot

import "github.com/sadbox/svc/telemetry"

// threadbot's 5-minute loops (sadbox_job_*{job_name}): the reminders' load
// retry and save, and the member counts' save. The 30s heartbeat is
// botutil's gateway_ready.
var (
	saveRemindersJob    = telemetry.NewJob("save_reminders")
	saveMemberCountsJob = telemetry.NewJob("save_member_counts")
)
