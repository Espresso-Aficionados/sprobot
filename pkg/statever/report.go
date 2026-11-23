package statever

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/Espresso-Aficionados/sprobot/pkg/buildinfo"
)

// ParseFlags parses a program's command line: -check-state, which reads
// what (as its usage says), and -json; and -version, which prints the
// build's version (buildinfo.Version) and exits 0 here. Anything else exits
// 2. Call it after healthcheck.Main, which looks at the first argument
// itself.
func ParseFlags(what string) (checkState, asJSON bool) {
	cs := flag.Bool("check-state", false, "read "+what+", creating or changing nothing; print each document's state version and this build's, then exit: 0 if this build can run on it, 3 if a newer build wrote some of it, 1 if some can't be read")
	js := flag.Bool("json", false, "with -check-state: print one JSON object instead")
	v := flag.Bool("version", false, "print this build's version (its short commit SHA) and exit")
	flag.Parse()
	if flag.NArg() > 0 || (*js && !*cs) || (*v && *cs) {
		flag.Usage()
		os.Exit(2)
	}
	if *v {
		fmt.Println(buildinfo.Version)
		os.Exit(0)
	}
	return *cs, *js
}

// Report is what -check-state found: the version of every state document a
// program reads, each read without being created or changed.
type Report struct {
	Program string
	Docs    []Doc
}

// Doc is one document's finding.
type Doc struct {
	Name    string `json:"name"`
	Exists  bool   `json:"exists"`
	Version int    `json:"version,omitempty"` // 0 if missing or unreadable
	Error   string `json:"error,omitempty"`   // why it couldn't be read, or is refused
	newer   bool
}

// Missing records a document that isn't there: the program starts it fresh.
func (r *Report) Missing(name string) {
	r.Docs = append(r.Docs, Doc{Name: name})
}

// Add records a JSON document as read, or the error reading it.
func (r *Report) Add(name string, data []byte, readErr error) {
	if readErr != nil {
		r.Found(name, 0, readErr)
		return
	}
	v, err := Of(name, data)
	r.Found(name, v, err)
}

// Found records a document whose version is known, or the error reading it
// (a *NewerError for one a newer build wrote).
func (r *Report) Found(name string, version int, err error) {
	d := Doc{Name: name, Exists: true}
	switch {
	case IsNewer(err):
		d.Version, d.newer, d.Error = version, true, err.Error()
	case err != nil:
		d.Error = err.Error()
	default:
		d.Version = version
	}
	r.Docs = append(r.Docs, d)
}

// check is -check-state -json's object. Its first five fields are the
// contract oncall-sync's -check-state -json has: the stored version (the
// highest found, 0 for none), this build's, the versions this build would
// upgrade the state through, the stored versions it doesn't know, and
// whether it can run on the state.
type check struct {
	StoreVersion int      `json:"store_version"`
	BuildVersion int      `json:"build_version"`
	Pending      []int    `json:"pending"`
	Unknown      []int    `json:"unknown"`
	Compatible   bool     `json:"compatible"`
	Unreadable   []string `json:"unreadable"`
	Documents    []Doc    `json:"documents"`
}

// Print writes the report and returns -check-state's exit code: 3 if a
// newer build wrote any document (whatever else failed: this build would
// refuse to start), else 1 if any couldn't be read, else 0. Missing
// documents are fine. Text goes one line per document to stdout, problems
// to stderr; with asJSON, stdout gets one JSON object instead.
func (r Report) Print(stdout, stderr io.Writer, asJSON bool) int {
	c := check{BuildVersion: Current, Pending: []int{}, Unknown: []int{}, Unreadable: []string{}, Documents: r.Docs}
	if c.Documents == nil {
		c.Documents = []Doc{}
	}
	for _, d := range r.Docs {
		c.StoreVersion = max(c.StoreVersion, d.Version)
		switch {
		case d.newer:
			if !slices.Contains(c.Unknown, d.Version) {
				c.Unknown = append(c.Unknown, d.Version)
			}
		case d.Error != "":
			c.Unreadable = append(c.Unreadable, d.Name)
		}
	}
	slices.Sort(c.Unknown)
	c.Compatible = len(c.Unknown) == 0
	if c.StoreVersion > 0 {
		for v := c.StoreVersion + 1; v <= Current; v++ {
			c.Pending = append(c.Pending, v)
		}
	}

	if asJSON {
		raw, _ := json.Marshal(c)
		fmt.Fprintf(stdout, "%s\n", raw)
	}
	for _, d := range r.Docs {
		switch {
		case d.Error != "" && strings.HasPrefix(d.Error, d.Name):
			fmt.Fprintln(stderr, d.Error)
		case d.Error != "":
			fmt.Fprintf(stderr, "%s: %s\n", d.Name, d.Error)
		case asJSON:
		case !d.Exists:
			fmt.Fprintf(stdout, "%s: none yet\n", d.Name)
		default:
			fmt.Fprintf(stdout, "%s: state version %d\n", d.Name, d.Version)
		}
	}
	build := fmt.Sprintf("%s (state version %d)", r.Program, Current)
	switch {
	case !c.Compatible:
		fmt.Fprintf(stderr, "%s: refuses state a newer build wrote (version %v)\n", build, c.Unknown)
		return 3
	case len(c.Unreadable) > 0:
		fmt.Fprintf(stderr, "%s: couldn't read %d of %d documents\n", build, len(c.Unreadable), len(r.Docs))
		return 1
	}
	if !asJSON {
		fmt.Fprintf(stdout, "%s: reads all %d documents\n", build, len(r.Docs))
	}
	return 0
}
