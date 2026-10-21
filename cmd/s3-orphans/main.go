// Command s3-orphans reports objects in the bots' bucket that nothing refers
// to any more, and deletes them when run with -delete:
//
//   - profile images whose profile is gone, or which their profile no longer
//     points at (left behind by a re-upload with another extension);
//   - sticky and shortcut files that the sticky and shortcut state doesn't
//     mention.
//
// mod_files/ is never listed or touched. Objects modified within minAge are
// skipped, since uploads are stored before the state that points at them.
//
// It reads the same S3_* variables as the bots:
//
//	set -a; . config/config.env; set +a
//	go run ./cmd/s3-orphans          # report only
//	go run ./cmd/s3-orphans -delete  # delete what it reports
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

const minAge = 24 * time.Hour

// Why an object is an orphan, in the order the summary lists them.
const (
	noProfile          = "profile image, no profile"
	unusedImage        = "profile image, not in use"
	unusedStickyFile   = "sticky file, unreferenced"
	unusedShortcutFile = "shortcut file, unreferenced"
)

var reasons = []string{noProfile, unusedImage, unusedStickyFile, unusedShortcutFile}

// stateDirs hold the sticky and shortcut state, one JSON file per guild.
var stateDirs = []string{"stickies", "shortcuts"}

type orphan struct {
	s3client.Object
	reason string
}

type sweep struct {
	orphans []orphan
	tooNew  int
	// Kept without a decision: keys no rule covers, and profiles whose image
	// field holds something other than one of the user's own uploads.
	unknownKeys     []string
	unclearProfiles int
	// Profiles under a template sprobot-web doesn't serve.
	retiredProfiles int
}

func main() {
	del := flag.Bool("delete", false, "delete the orphans found instead of only reporting them")
	flag.Parse()

	c, err := s3client.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := run(context.Background(), c, os.Stdout, *del, time.Now()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, c *s3client.Client, out io.Writer, del bool, now time.Time) error {
	s, err := findOrphans(ctx, c, now)
	if err != nil {
		return err
	}
	s.print(out)
	if !del {
		fmt.Fprintln(out, "Dry run: nothing was deleted. Run with -delete to delete the orphans listed.")
		return nil
	}
	keys := make([]string, len(s.orphans))
	for i, o := range s.orphans {
		keys[i] = o.Key
	}
	if err := c.DeleteKeys(ctx, keys...); err != nil {
		return err
	}
	fmt.Fprintf(out, "Deleted %d objects.\n", len(keys))
	return nil
}

func findOrphans(ctx context.Context, c *s3client.Client, now time.Time) (*sweep, error) {
	s := &sweep{}
	if err := s.profileImages(ctx, c, now); err != nil {
		return nil, err
	}
	refs, err := stateText(ctx, c)
	if err != nil {
		return nil, err
	}
	if err := s.savedFiles(ctx, c, sprobot.FileKindSticky, unusedStickyFile, refs, now); err != nil {
		return nil, err
	}
	if err := s.savedFiles(ctx, c, sprobot.FileKindShortcut, unusedShortcutFile, refs, now); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *sweep) profileImages(ctx context.Context, c *s3client.Client, now time.Time) error {
	images, err := c.ListObjects(ctx, "images/")
	if err != nil {
		return err
	}
	profileObjects, err := c.ListObjects(ctx, "profiles/")
	if err != nil {
		return err
	}
	profiles := make(map[string]bool, len(profileObjects))
	for _, p := range profileObjects {
		profiles[p.Key] = true
		if parts := strings.Split(p.Key, "/"); len(parts) == 4 {
			if _, known := knownTemplate(parts[2]); !known {
				s.retiredProfiles++
			}
		}
	}

	for _, img := range images {
		guildID, tmplName, userID, ok := sprobot.ParseProfileImageKey(img.Key)
		if !ok {
			s.unknownKeys = append(s.unknownKeys, img.Key)
			continue
		}
		if now.Sub(img.LastModified) < minAge {
			s.tooNew++
			continue
		}
		if !profiles[sprobot.ProfileS3Path(guildID, tmplName, userID)] {
			s.add(img, noProfile)
			continue
		}

		profile, err := c.FetchProfileSimple(ctx, guildID, tmplName, userID)
		if errors.Is(err, s3client.ErrNotFound) {
			s.add(img, noProfile) // deleted since the listing
			continue
		}
		if err != nil {
			return err
		}
		tmpl, _ := knownTemplate(tmplName)
		inUse, ok := c.ProfileImageKey(tmpl, guildID, userID, profile)
		switch {
		case ok && inUse == img.Key:
			// The current image.
		case ok || profile[tmpl.Image.Name] == "":
			s.add(img, unusedImage)
		default:
			// An external URL or an old form of link: leave it to a person.
			s.unclearProfiles++
		}
	}
	return nil
}

// savedFiles sweeps one kind of saved file. A file counts as referenced if its
// name appears anywhere in the sticky or shortcut state: as a bare key, a
// bucket URL or a web route. Its first 30 characters are random, so they can't
// match by accident, and URL escaping never changes them.
func (s *sweep) savedFiles(ctx context.Context, c *s3client.Client, kind, reason, refs string, now time.Time) error {
	prefix, _ := sprobot.FilePrefix(kind)
	files, err := c.ListObjects(ctx, prefix+"/")
	if err != nil {
		return err
	}
	for _, f := range files {
		_, _, name, ok := sprobot.ParseFileKey(f.Key)
		if !ok {
			s.unknownKeys = append(s.unknownKeys, f.Key)
			continue
		}
		if now.Sub(f.LastModified) < minAge {
			s.tooNew++
			continue
		}
		if !strings.Contains(refs, name[:30]) {
			s.add(f, reason)
		}
	}
	return nil
}

// stateText returns every sticky and shortcut state file, concatenated.
func stateText(ctx context.Context, c *s3client.Client) (string, error) {
	var b strings.Builder
	for _, dir := range stateDirs {
		objects, err := c.ListObjects(ctx, dir+"/")
		if err != nil {
			return "", err
		}
		for _, o := range objects {
			name, ok := strings.CutSuffix(strings.TrimPrefix(o.Key, dir+"/"), ".json")
			if !ok {
				continue
			}
			data, err := c.FetchGuildJSON(ctx, dir, name)
			if err != nil {
				return "", fmt.Errorf("reading %s: %w", o.Key, err)
			}
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// knownTemplate returns the template sprobot uses for name. Unknown names get
// a template with the image field every template has used.
func knownTemplate(name string) (sprobot.Template, bool) {
	for _, tmpls := range sprobot.AllTemplates() {
		for _, tmpl := range tmpls {
			if tmpl.Name == name {
				return tmpl, true
			}
		}
	}
	return sprobot.Template{Name: name, Image: sprobot.Field{Name: sprobot.ImageField}}, false
}

func (s *sweep) add(o s3client.Object, reason string) {
	s.orphans = append(s.orphans, orphan{Object: o, reason: reason})
}

func (s *sweep) print(out io.Writer) {
	for _, o := range s.orphans {
		fmt.Fprintf(out, "%-28s %10s  %s\n", o.reason, sprobot.FormatBytes(o.Size), o.Key)
	}
	for _, key := range s.unknownKeys {
		fmt.Fprintf(out, "%-28s %10s  %s\n", "kept, unrecognized key", "", key)
	}

	fmt.Fprintln(out, "\nSummary:")
	var total int64
	for _, reason := range reasons {
		n, size := 0, int64(0)
		for _, o := range s.orphans {
			if o.reason == reason {
				n++
				size += o.Size
			}
		}
		total += size
		fmt.Fprintf(out, "  %-40s %6d (%s)\n", reason+":", n, sprobot.FormatBytes(size))
	}
	fmt.Fprintf(out, "  %-40s %6d (%s)\n", "orphans:", len(s.orphans), sprobot.FormatBytes(total))
	fmt.Fprintf(out, "  %-40s %6d\n", "skipped, modified in the last day:", s.tooNew)
	fmt.Fprintf(out, "  %-40s %6d\n", "kept, unrecognized key:", len(s.unknownKeys))
	fmt.Fprintf(out, "  %-40s %6d\n", "kept, profile image field unclear:", s.unclearProfiles)
	fmt.Fprintf(out, "  %-40s %6d (sprobot-web no longer serves them)\n", "profiles under retired templates:", s.retiredProfiles)
}
