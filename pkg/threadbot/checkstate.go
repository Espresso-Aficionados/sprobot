package threadbot

import (
	"context"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/s3client"
	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
)

// CheckState is threadbot's -check-state: every allowed guild's reminders,
// member counts and hidden threads, read as startup reads them, with nothing
// created or changed.
func CheckState(ctx context.Context, s3 *s3client.Client) statever.Report {
	r := statever.Report{Program: "threadbot"}
	botutil.CheckGuildState(ctx, &r, s3, s3client.ThreadRemindersPrefix, s3client.ThreadMemberCountsPrefix, s3client.HiddenThreadsPrefix)
	return r
}
