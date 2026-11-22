// Command specs-index indexes Markdown requirement and design documents and
// Gherkin .feature files, then searches, traces and checks coverage over them.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"

	"github.com/wamra/specs-index/internal/api"
	"github.com/wamra/specs-index/internal/config"
	"github.com/wamra/specs-index/internal/store"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = ""

func cmdVersion(_ []string, out, _ io.Writer) int {
	v := version
	if v == "" {
		v = "dev"
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
	}
	fmt.Fprintln(out, "specs-index", v)
	return 0
}

type command struct {
	run  func(args []string, out, errw io.Writer) int
	help string
}

var commands map[string]command

func init() {
	commands = map[string]command{
		"add":      {cmdAdd, "add PATH...                 register files or directories (inside the project root) as sources"},
		"remove":   {cmdRemove, "remove PATH...              unregister sources and drop the documents they alone covered"},
		"sources":  {cmdSources, "sources                     list the registered sources"},
		"index":    {cmdIndex, "index [--reembed] [--infer] [--allow-cloud] [--no-embed]"},
		"search":   {cmdSearch, "search QUERY [--mode auto|exact|fts|vector|hybrid] [--k 10]   (use -- before a query starting with -)"},
		"show":     {cmdShow, "show KEY                   the entity with its source lines and direct edges"},
		"trace":    {cmdTrace, "trace KEY [--direction down|up|both] [--depth 5] [--kinds a,b] [--include-inferred]"},
		"coverage": {cmdCoverage, "coverage                    requirements, designs and scenarios with gaps; dangling, duplicate and conflicting IDs"},
		"status":   {cmdStatus, "status                      index counts and the configured embedder"},
		"doctor":   {cmdDoctor, "doctor                      check the index and the embedder; exit 1 on a failure"},
		"config":   {cmdConfig, "config [init]               show the effective configuration, or write an example file"},
		"version":  {cmdVersion, "version"},
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: specs-index <command> [flags]   (every command except version takes --root DIR and --json)")
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintln(w, "  "+commands[n].help)
	}
}

func run(args []string, out, errw io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(errw)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	c, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(errw, "specs-index: unknown command %q\n", args[0])
		usage(errw)
		return 2
	}
	return c.run(args[1:], out, errw)
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// common holds the flags every command shares.
type common struct {
	root string
	json bool
}

func newFlags(name string, errw io.Writer, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errw)
	fs.StringVar(&c.root, "root", "", "project root (default: nearest ancestor of the working directory with .git, else the working directory)")
	fs.BoolVar(&c.json, "json", false, "print the result as JSON")
	return fs
}

// parseInterspersed parses flags that may appear before or after positional
// arguments; everything after a "--" is positional. It returns the positionals
// and an exit code (-1 to carry on, 0 for -h, 2 for a usage error).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, int) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			tail = args[i+1:]
			args = args[:i]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0
			}
			return nil, 2
		}
		args = fs.Args()
		if len(args) == 0 {
			return append(pos, tail...), -1
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// usageErr prints a usage problem and returns exit code 2.
func usageErr(errw io.Writer, format string, a ...any) int {
	fmt.Fprintf(errw, "specs-index: "+format+"\n", a...)
	return 2
}

// fail prints an error and returns exit code 1.
func fail(errw io.Writer, err error) int {
	fmt.Fprintf(errw, "specs-index: %v\n", err)
	return 1
}

func printJSON(out io.Writer, v any) int {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}

func (c common) resolveRoot() (string, error) { return api.ResolveRoot(c.root) }

// openExisting opens the index of the project for reading.
func (c common) openExisting() (*store.Store, string, error) {
	root, err := c.resolveRoot()
	if err != nil {
		return nil, "", err
	}
	st, err := api.OpenExisting(root)
	return st, root, err
}

func loadConfig() (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}
