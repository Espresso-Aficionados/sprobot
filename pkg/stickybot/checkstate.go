package stickybot

import (
	"context"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/s3client"
	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
)

// CheckState is stickybot's -check-state: every allowed guild's stickies,
// read as startup reads them, with nothing created or changed.
func CheckState(ctx context.Context, s3 *s3client.Client) statever.Report {
	r := statever.Report{Program: "stickybot"}
	botutil.CheckGuildState(ctx, &r, s3, s3client.StickiesPrefix)
	return r
}
