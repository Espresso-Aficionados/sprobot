package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/s3client"
	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
)

// CheckState is sprobot's -check-state: the state startup loads, read the
// way startup reads it, with nothing created or changed. That is every
// store's object for every allowed guild, and the cache dir.
func CheckState(ctx context.Context, s3 *s3client.Client) statever.Report {
	return checkState(ctx, s3, defaultCacheDir)
}

func checkState(ctx context.Context, s3 *s3client.Client, cacheDir string) statever.Report {
	r := statever.Report{Program: "sprobot"}
	// The stores only, for their prefixes: nothing here loads or saves.
	b := &Bot{BaseBot: &botutil.BaseBot{S3: s3, Log: slog.Default()}}
	b.initStateStores()
	var prefixes []string
	for _, s := range b.stateStores() {
		prefixes = append(prefixes, s.prefix())
	}
	botutil.CheckGuildState(ctx, &r, s3, prefixes...)

	vpath := filepath.Join(cacheDir, cacheVersionFile)
	v, exists, err := cacheDirVersion(cacheDir)
	if !exists {
		r.Missing(vpath)
	} else {
		r.Found(vpath, v, err)
	}
	if statever.IsNewer(err) {
		return r // the cache files are in a layout this build doesn't know
	}
	for _, name := range cacheFiles {
		path := filepath.Join(cacheDir, name)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			r.Missing(path)
			continue
		}
		err := readCacheFile(cacheDir, name, func(json.RawMessage) {})
		r.Found(path, 1, err)
	}
	return r
}
