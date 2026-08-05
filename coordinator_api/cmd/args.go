package cmd

import (
	"strings"

	"github.com/urfave/cli/v2"
)

// NormalizeArgs reorders argv so that flags placed after a positional
// argument are still parsed.
//
// urfave/cli v2 parses with the stdlib "flag" package, which stops at the
// first non-flag token and offers no "permute" mode. The natural invocation
//
//	reactorcide jobs get <job-id> --format json
//
// therefore drops --format as a trailing positional instead of parsing it.
// NormalizeArgs walks the command tree to find the command argv resolves to,
// collecting the flags visible at each level, then moves that command's
// recognized flags ahead of its positional arguments — the ordering the
// underlying parser already handles.
//
// Unknown flags are left in place as flags, so a typo still produces the
// normal "flag provided but not defined" error rather than being silently
// swallowed.
func NormalizeArgs(app *cli.App, args []string) []string {
	if len(args) < 2 {
		return args
	}

	visible := append([]cli.Flag{}, app.Flags...)
	commands := app.Commands

	i := 1
	for i < len(args) {
		token := args[i]
		if token == "--" {
			return args
		}
		if token != "-" && strings.HasPrefix(token, "-") {
			i++
			name := strings.TrimLeft(token, "-")
			if !strings.Contains(name, "=") && !isBoolFlag(visible, name) && i < len(args) {
				i++ // this flag's value token
			}
			continue
		}

		cmd := lookupCommand(commands, token)
		if cmd == nil {
			break // first positional argument of the resolved command
		}
		visible = append(visible, cmd.Flags...)
		commands = cmd.Subcommands
		i++
	}

	if i >= len(args) {
		return args
	}
	head := append([]string{}, args[:i]...)
	return append(head, reorderFlagsBeforeArgs(visible, args[i:])...)
}

func lookupCommand(commands []*cli.Command, name string) *cli.Command {
	for _, cmd := range commands {
		if cmd.Name == name {
			return cmd
		}
		for _, alias := range cmd.Aliases {
			if alias == name {
				return cmd
			}
		}
	}
	return nil
}

func isBoolFlag(flags []cli.Flag, name string) bool {
	if name == "help" || name == "h" {
		return true
	}
	for _, f := range flags {
		if _, ok := f.(*cli.BoolFlag); !ok {
			continue
		}
		for _, n := range f.Names() {
			if n == name {
				return true
			}
		}
	}
	return false
}

// reorderFlagsBeforeArgs returns the flag tokens of args followed by its bare
// positional tokens, preserving relative order within each group. Tokens from
// a literal "--" onward stay together as positionals. An unrecognized "-x" is
// treated as value-taking, matching every non-boolean flag in this CLI.
func reorderFlagsBeforeArgs(flags []cli.Flag, args []string) []string {
	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") || isBoolFlag(flags, name) {
			continue
		}
		if i+1 < len(args) {
			flagArgs = append(flagArgs, args[i+1])
			i++
		}
	}
	return append(flagArgs, positional...)
}
