package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Argument parsing for grafana-cli.
//
// The parser is deliberately strict. This CLI is driven mostly by agents, and a
// mistyped flag that is silently ignored produces a plausible but wrong answer —
// the most expensive failure mode there is. Unknown flags, missing values,
// out-of-range enum values, malformed durations and unexpected positional
// arguments are all hard errors that name the accepted alternatives.

// errHelp signals that the caller asked for the command's usage text.
var errHelp = errors.New("help requested")

type flagKind int

const (
	flagAny  flagKind = iota // no validation beyond the enum, if any
	flagTime                 // --start/--end/--time: duration, epoch or RFC3339
	flagStep                 // --step: duration or bare seconds
	flagInt                  // positive whole number
)

// flagSpec describes one accepted flag.
type flagSpec struct {
	name string // including the leading dashes, e.g. "--start"
	desc string // short description, shown when the parser rejects input
	kind flagKind
	enum []string // allowed values, when the flag is an enumeration
}

func flag(name, desc string) flagSpec     { return flagSpec{name: name, desc: desc} }
func timeFlag(name, desc string) flagSpec { return flagSpec{name: name, desc: desc, kind: flagTime} }
func stepFlag(name, desc string) flagSpec { return flagSpec{name: name, desc: desc, kind: flagStep} }
func intFlag(name, desc string) flagSpec  { return flagSpec{name: name, desc: desc, kind: flagInt} }

func enumFlag(name, desc string, values ...string) flagSpec {
	return flagSpec{name: name, desc: desc, enum: values}
}

// formatFlag is shared by every command that can emit machine-readable output.
func formatFlag() flagSpec {
	return enumFlag("--format", "output format", "table", "tsv")
}

// parsedArgs is the result of parsing one command's arguments.
type parsedArgs struct {
	positional []string
	flags      map[string]string
}

// pos returns the i-th positional argument. parseArgs has already guaranteed
// the count, so this cannot be out of range for a validated command.
func (p *parsedArgs) pos(i int) string { return p.positional[i] }

// str returns a flag value, or "" when the flag was not supplied.
func (p *parsedArgs) str(name string) string { return p.flags[name] }

// intOr returns the value of a numeric flag, or def when it was not supplied.
// The value was already validated by parseArgs.
func (p *parsedArgs) intOr(name string, def int) int {
	raw, ok := p.flags[name]
	if !ok || raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// parseArgs splits args into exactly wantPos positional arguments plus the
// flags described by specs. usage is echoed whenever the input is rejected.
func parseArgs(usage string, args []string, wantPos int, specs ...flagSpec) (*parsedArgs, error) {
	known := make(map[string]flagSpec, len(specs))
	for _, s := range specs {
		known[s.name] = s
	}

	p := &parsedArgs{flags: map[string]string{}}
	endOfFlags := false

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if endOfFlags {
			p.positional = append(p.positional, arg)
			continue
		}
		switch {
		case arg == "--":
			endOfFlags = true
			continue
		case arg == "-h" || arg == "--help":
			return nil, errHelp
		case !strings.HasPrefix(arg, "--"):
			// Anything that is not --flag is a positional argument, so that
			// negative numbers and queries such as {a="-1"} are not mistaken
			// for flags.
			p.positional = append(p.positional, arg)
			continue
		}

		name, value, hasValue := strings.Cut(arg, "=")
		spec, ok := known[name]
		if !ok {
			return nil, fmt.Errorf("unknown flag %q\n%s\n  %s", name, knownFlagsHint(specs), usage)
		}
		if _, dup := p.flags[name]; dup {
			return nil, fmt.Errorf("flag %s given more than once", name)
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s needs a value (%s)\n  %s", name, spec.desc, usage)
			}
			next := args[i+1]
			if strings.HasPrefix(next, "--") {
				// Almost certainly a forgotten value rather than a value that
				// happens to start with two dashes.
				return nil, fmt.Errorf("flag %s needs a value (%s), but the next argument %q is another flag\n  use %s=<value> if the value really starts with --",
					name, spec.desc, next, name)
			}
			i++
			value = next
		}
		if err := validateFlag(spec, value); err != nil {
			return nil, err
		}
		p.flags[name] = value
	}

	if len(p.positional) != wantPos {
		return nil, fmt.Errorf("%s\n  %s", positionalError(p.positional, wantPos), usage)
	}
	return p, nil
}

func validateFlag(spec flagSpec, value string) error {
	if len(spec.enum) > 0 && !contains(spec.enum, value) {
		return fmt.Errorf("invalid value %q for %s; expected one of: %s",
			value, spec.name, strings.Join(spec.enum, ", "))
	}
	switch spec.kind {
	case flagTime:
		return validateTimeArg(spec.name, value)
	case flagStep:
		return validateStepArg(spec.name, value)
	case flagInt:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid value %q for %s: expected a whole number", value, spec.name)
		}
		if n <= 0 {
			return fmt.Errorf("invalid value %d for %s: expected a number greater than 0", n, spec.name)
		}
	}
	return nil
}

func positionalError(got []string, want int) string {
	if len(got) < want {
		return fmt.Sprintf("expected %d argument(s), got %d", want, len(got))
	}
	msg := fmt.Sprintf("expected %d argument(s), got %d: %s",
		want, len(got), strings.Join(quoteAll(got), " "))
	if want > 0 {
		// The classic mistake: an unquoted PromQL/LogQL query that the shell
		// split into several words.
		msg += "\n  a query containing spaces must be quoted, e.g. 'sum(rate(x[5m])) by (job)'"
	}
	return msg
}

func quoteAll(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strconv.Quote(v)
	}
	return out
}

func knownFlagsHint(specs []flagSpec) string {
	if len(specs) == 0 {
		return "  this command takes no flags"
	}
	var sb strings.Builder
	sb.WriteString("  accepted flags:")
	for _, s := range specs {
		fmt.Fprintf(&sb, "\n    %-11s %s", s.name, s.desc)
		if len(s.enum) > 0 {
			fmt.Fprintf(&sb, " (one of: %s)", strings.Join(s.enum, ", "))
		}
	}
	return sb.String()
}

func contains(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}
