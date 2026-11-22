package bot

import "github.com/sadbox/svc/telemetry"

// sprobot's background loops, one Job each (sadbox_job_*{job_name}), made
// before telemetry.Start so its first export reports them at 0. The 30s
// heartbeat is botutil's gateway_ready.
var (
	retryFailedLoadsJob        = telemetry.NewJob("retry_failed_loads")         // 1m
	processTempRoleExpiriesJob = telemetry.NewJob("process_temp_role_expiries") // 1m
	saveTopPostersJob          = telemetry.NewJob("save_top_posters")           // 5m
	savePosterRoleJob          = telemetry.NewJob("save_poster_role")           // 5m
	saveShortcutsJob           = telemetry.NewJob("save_shortcuts")             // 5m
	saveTicketsJob             = telemetry.NewJob("save_tickets")               // 5m
	saveWelcomeJob             = telemetry.NewJob("save_welcome")               // 5m
	saveCachesJob              = telemetry.NewJob("save_caches")                // 5m
	saveStarboardJob           = telemetry.NewJob("save_starboard")             // 5m
	retryStarboardDeletesJob   = telemetry.NewJob("retry_starboard_deletes")    // 5m
	saveRenameLogsJob          = telemetry.NewJob("save_rename_logs")           // 5m
	saveTempRolesJob           = telemetry.NewJob("save_temp_roles")            // 5m
	pruneCorruptBackupsJob     = telemetry.NewJob("prune_corrupt_backups")      // 1h
	forumRemindersJob          = telemetry.NewJob("forum_reminders")            // 30m
)

// componentNames and modalNames are the custom-ID prefixes interactions are
// labelled by (botutil.TraceComponents, TraceModals): the routes of
// handleComponentInteraction and handleModalSubmit. A custom ID matching
// none is "other".
var (
	componentNames = []string{
		"selfrole", "ticket_open", "ticket_close", "ticket_close_yes", "ticket_close_no",
		"del_cancel", "del_profile", "del_image", "del_confirm_profile", "del_confirm_image",
	}
	modalNames = []string{"edit", "sconfig_set", "welcome_set", "modlog"}
)
