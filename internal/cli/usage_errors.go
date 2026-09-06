package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// usageError marks an error raised by this package's own validation inside
// a command handler, so that Main reports it with ExitUsage. Errors raised
// before a handler starts (flag parsing, argument checks, configuration)
// need no mark; see exitCode.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

func usage(err error) error { return &usageError{err: err} }

// The flag parser follows the POSIX convention: one dash introduces
// single-letter flags, so "-camera X" is read as the shorthand -c with the
// value "amera", and X becomes a positional argument. The resulting errors
// ("accepts at most 1 arg") hide the real mistake. The helpers below detect
// the common cases and say what to type instead.

// misreadFlag reports a flag whose value is its own name minus the first
// letter, which is what "-camera" leaves behind.
func misreadFlag(cmd *cobra.Command, extra []string) error {
	var err error
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if err != nil || f.Shorthand == "" || !f.Changed || len(f.Name) < 2 {
			return
		}
		if f.Value.String() != f.Name[1:] {
			return
		}
		msg := "-" + f.Name + " was read as -" + f.Shorthand + " with the value " + strconv.Quote(f.Name[1:]) + "; flags take two dashes: --" + f.Name
		if len(extra) > 0 {
			msg += " " + strconv.Quote(extra[0])
		}
		err = errors.New(msg)
	})
	return err
}

// dirArgs validates the optional single DIR argument with a message that
// names the offending argument and, when applicable, the flag mistake that
// produced it.
func dirArgs(cmd *cobra.Command, args []string) error {
	if len(args) <= 1 {
		return nil
	}
	if err := misreadFlag(cmd, args); err != nil {
		return err
	}
	quoted := make([]string, 0, len(args)-1)
	for _, a := range args[1:] {
		quoted = append(quoted, strconv.Quote(a))
	}
	return fmt.Errorf("unexpected argument %s: only one DIR may be given (after %q)", strings.Join(quoted, ", "), args[0])
}

var unknownShorthandRe = regexp.MustCompile(`unknown shorthand flag: '.' in -([^ ]+)`)

// flagError adds a hint to the parser's error when a long flag was written
// with a single dash, e.g. "-by 2m" or "-dry-run".
func flagError(cmd *cobra.Command, err error) error {
	m := unknownShorthandRe.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	rest := m[1]
	var hint string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if hint != "" || len(f.Name) < 2 {
			return
		}
		// "-by" fails on 'b' with rest "by"; "-recursive" first consumes the
		// valid shorthand -r and then fails on 'e' with rest "ecursive".
		if f.Name == rest || (strings.HasSuffix(f.Name, rest) && len(f.Name) == len(rest)+1) {
			hint = f.Name
		}
	})
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w; flags take two dashes: --%s", err, hint)
}
