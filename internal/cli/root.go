// Package cli wires the application layer to a cobra command tree. It owns
// flag parsing, configuration loading and output formatting, and nothing
// else: every action is a message handled by package app.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sollniss/camsync/app"
	"github.com/sollniss/camsync/internal/config"
)

// Exit codes returned by Main, following the common convention that 2 means
// the command line itself was wrong.
const (
	ExitOK = 0
	// ExitFailure: the command was valid but the work failed, e.g. the
	// directory could not be read or some sidecars could not be written.
	ExitFailure = 1
	// ExitUsage: the invocation was invalid: unknown command or flag, bad
	// argument or value (including a --camera that matches nothing),
	// invalid configuration.
	ExitUsage = 2
	// ExitInterrupted: the command was cancelled, normally by Ctrl-C.
	ExitInterrupted = 130
)

// Main runs the CLI with the given arguments and returns the process exit
// code. Output goes to stdout, diagnostics to stderr.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	rt, root := newRoot()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return ExitOK
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
	code := exitCode(err, rt.ran)
	if code == ExitUsage {
		fmt.Fprintf(stderr, "Run '%s --help' for usage.\n", cmd.CommandPath())
	}
	return code
}

// exitCode classifies err. Anything that goes wrong before a handler starts
// (flag parsing, argument checks, configuration, cobra's own validation) is
// a usage error; inside a handler, errors marked by usage or reported as an
// app.ValidationError are usage errors and everything else is a failure.
func exitCode(err error, handlerRan bool) int {
	var usageErr *usageError
	var validationErr *app.ValidationError
	switch {
	case errors.Is(err, context.Canceled):
		return ExitInterrupted
	case !handlerRan, errors.As(err, &usageErr), errors.As(err, &validationErr):
		return ExitUsage
	}
	return ExitFailure
}

// runtime carries the state shared by the commands of one invocation.
type runtime struct {
	cfgFile string
	cfg     *config.Config
	ran     bool // a command handler was started; see exitCode
}

// runE wraps a command's RunE so that Main can tell errors raised before the
// handler started from those raised inside it.
func (rt *runtime) runE(fn func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		rt.ran = true
		return fn(cmd, args)
	}
}

// NewRootCommand builds the command tree. Nothing is global, so several
// independent instances can exist, which keeps tests isolated.
func NewRootCommand() *cobra.Command {
	_, root := newRoot()
	return root
}

func newRoot() (*runtime, *cobra.Command) {
	rt := &runtime{}
	root := &cobra.Command{
		Use:   "camsync",
		Short: "Shift photo timestamps per camera using XMP sidecars",
		Long: `camsync fixes the clock of a camera after the fact.

It groups the photos of a directory by camera and shifts the timestamps of
one camera by a given amount so that photos from several cameras sort in the
right order. Image files are never modified: all changes go to XMP sidecar
files, which Immich, Lightroom, darktable, digiKam and exiftool read.

Settings can also come from ` + configHint() + ` or from
environment variables such as ` + config.EnvPrefix + `_SIDECAR_STYLE. Flags
override both.`,
		Example: `  camsync list ~/Photos/trip
  camsync suggest ~/Photos/trip
  camsync shift --camera sony --by 2m30s ~/Photos/trip
  camsync shift --camera 2 --timezone +02:00 --dry-run ~/Photos/trip
  camsync reset --camera sony ~/Photos/trip`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := misreadFlag(cmd, nil); err != nil {
				return err
			}
			// Generating completion scripts and answering completion
			// requests must not depend on the configuration being valid.
			if isCompletionCommand(cmd) {
				return nil
			}
			cfg, err := config.Load(cmd.Flags(), rt.cfgFile)
			if err != nil {
				return err
			}
			rt.cfg = cfg
			return nil
		},
	}
	root.SetFlagErrorFunc(flagError)
	pf := root.PersistentFlags()
	pf.StringVar(&rt.cfgFile, "config", "", "config file (default "+configHint()+")")
	pf.BoolP("recursive", "r", false, "also scan sub-directories")
	pf.String("sidecar-style", "ext", `naming of new sidecars: "ext" (IMG_0001.JPG.xmp; Immich, exiftool, darktable) or "name" (IMG_0001.xmp; Adobe, digiKam)`)
	pf.StringP("output", "o", "table", `output format: "table" for people or "json" for scripts`)

	registerCompletions(root, map[string]cobra.CompletionFunc{
		"sidecar-style": cobra.FixedCompletions([]cobra.Completion{
			cobra.CompletionWithDesc("ext", "IMG_0001.JPG.xmp (Immich, exiftool, darktable)"),
			cobra.CompletionWithDesc("name", "IMG_0001.xmp (Adobe, digiKam)"),
		}, cobra.ShellCompDirectiveNoFileComp),
		"output": cobra.FixedCompletions([]cobra.Completion{
			cobra.CompletionWithDesc("table", "human-readable"),
			cobra.CompletionWithDesc("json", "for scripts"),
		}, cobra.ShellCompDirectiveNoFileComp),
	})
	if err := root.MarkPersistentFlagFilename("config", "yaml", "yml", "json"); err != nil {
		panic(err)
	}

	root.AddCommand(rt.listCommand(), rt.suggestCommand(), rt.shiftCommand(), rt.localizeCommand(), rt.geotagCommand(), rt.resetCommand())
	return rt, root
}

// isCompletionCommand reports whether cmd is cobra's "completion" script
// generator (or one of its shell sub-commands) or the hidden "__complete"
// command that shells call to request completions.
func isCompletionCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil && c.HasParent(); c = c.Parent() {
		switch c.Name() {
		case "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return true
		}
	}
	return false
}

func configHint() string {
	dir, err := config.Dir()
	if err != nil {
		return config.FileName + ".yaml in the user config directory"
	}
	return filepath.Join(dir, config.FileName+".yaml")
}

// scope builds the photo selection from the optional DIR argument and the
// configuration.
func (rt *runtime) scope(args []string) app.Scope {
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	return app.Scope{Dir: dir, Recursive: rt.cfg.Recursive, SidecarStyle: rt.cfg.Style()}
}

func (rt *runtime) json() bool { return rt.cfg.Output == "json" }
