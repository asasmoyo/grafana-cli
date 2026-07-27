package main

import (
	"errors"
	"strings"
	"testing"
)

// promRangeSpecs mirrors the flags of `prom query-range`, the command with the
// widest flag surface, and is reused as a realistic fixture.
func promRangeSpecs() []flagSpec {
	return []flagSpec{
		timeFlag("--start", "window start"),
		timeFlag("--end", "window end"),
		stepFlag("--step", "resolution"),
		formatFlag(),
	}
}

func TestParseArgsPositional(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantPos int
		pos     []string
		errHas  string
	}{
		{
			name:    "exact count",
			args:    []string{"Prometheus", "up"},
			wantPos: 2,
			pos:     []string{"Prometheus", "up"},
		},
		{
			name:    "positional split by flags",
			args:    []string{"--start", "1h", "Prometheus", "--step", "5m", "up"},
			wantPos: 2,
			pos:     []string{"Prometheus", "up"},
		},
		{
			name:    "too few",
			args:    []string{"Prometheus"},
			wantPos: 2,
			errHas:  "expected 2 argument(s), got 1",
		},
		{
			name:    "too many is not silently ignored",
			args:    []string{"Prometheus", "sum(rate(x[5m]))", "by", "(job)"},
			wantPos: 2,
			errHas:  "a query containing spaces must be quoted",
		},
		{
			name:    "value starting with dash stays positional",
			args:    []string{"Prometheus", "-1"},
			wantPos: 2,
			pos:     []string{"Prometheus", "-1"},
		},
		{
			name:    "double dash terminator",
			args:    []string{"Prometheus", "--", "--weird-query"},
			wantPos: 2,
			pos:     []string{"Prometheus", "--weird-query"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseArgs("usage: test", tt.args, tt.wantPos, promRangeSpecs()...)
			if tt.errHas != "" {
				requireErrContains(t, err, tt.errHas)
				return
			}
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			if strings.Join(p.positional, "\x00") != strings.Join(tt.pos, "\x00") {
				t.Fatalf("positional = %q, want %q", p.positional, tt.pos)
			}
		})
	}
}

func TestParseArgsFlagForms(t *testing.T) {
	t.Run("space separated", func(t *testing.T) {
		p := mustParseArgs(t, []string{"ds", "q", "--start", "1h"}, 2)
		if got := p.str("--start"); got != "1h" {
			t.Fatalf("--start = %q, want 1h", got)
		}
	})

	t.Run("equals separated", func(t *testing.T) {
		p := mustParseArgs(t, []string{"ds", "q", "--start=1h", "--format=tsv"}, 2)
		if got := p.str("--start"); got != "1h" {
			t.Fatalf("--start = %q, want 1h", got)
		}
		if got := p.str("--format"); got != "tsv" {
			t.Fatalf("--format = %q, want tsv", got)
		}
	})

	t.Run("equals form accepts a value starting with dashes", func(t *testing.T) {
		p, err := parseArgs("usage: test", []string{"ds", "--query=--odd"}, 1,
			flag("--query", "TraceQL selector"))
		if err != nil {
			t.Fatalf("parseArgs: %v", err)
		}
		if got := p.str("--query"); got != "--odd" {
			t.Fatalf("--query = %q, want --odd", got)
		}
	})

	t.Run("unset flag is empty", func(t *testing.T) {
		p := mustParseArgs(t, []string{"ds", "q"}, 2)
		if got := p.str("--start"); got != "" {
			t.Fatalf("--start = %q, want empty", got)
		}
	})
}

func TestParseArgsRejectsBadFlags(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		errHas string
	}{
		{
			name:   "unknown flag",
			args:   []string{"ds", "q", "--steps", "5m"},
			errHas: `unknown flag "--steps"`,
		},
		{
			name:   "unknown flag lists the accepted ones",
			args:   []string{"ds", "q", "--steps", "5m"},
			errHas: "--step",
		},
		{
			name:   "missing value at end",
			args:   []string{"ds", "q", "--start"},
			errHas: "flag --start needs a value",
		},
		{
			name:   "value swallowed by the next flag",
			args:   []string{"ds", "q", "--start", "--format", "tsv"},
			errHas: "is another flag",
		},
		{
			name:   "duplicate flag",
			args:   []string{"ds", "q", "--start", "1h", "--start", "2h"},
			errHas: "given more than once",
		},
		{
			name:   "invalid enum value",
			args:   []string{"ds", "q", "--format", "json"},
			errHas: `invalid value "json" for --format; expected one of: table, tsv`,
		},
		{
			name:   "empty enum value",
			args:   []string{"ds", "q", "--format="},
			errHas: "invalid value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseArgs("usage: test", tt.args, 2, promRangeSpecs()...)
			requireErrContains(t, err, tt.errHas)
		})
	}

	t.Run("command without flags", func(t *testing.T) {
		_, err := parseArgs("usage: test", []string{"ds", "--format", "tsv"}, 1)
		requireErrContains(t, err, "this command takes no flags")
	})
}

func TestParseArgsValidatesTimeFlags(t *testing.T) {
	valid := []string{"90s", "30m", "1h", "1h30m", "2d", "1w", "1700000000", "2026-07-27T10:00:00Z"}
	for _, v := range valid {
		t.Run("valid/"+v, func(t *testing.T) {
			if _, err := parseArgs("usage: test", []string{"ds", "q", "--start", v}, 2, promRangeSpecs()...); err != nil {
				t.Fatalf("--start %s rejected: %v", v, err)
			}
		})
	}

	// These used to be forwarded verbatim to the datasource, which answered
	// with an opaque parse error or, worse, a silently different window.
	invalid := []string{"1hour", "90sec", "yesterday", "1h ago", "-1h", "1.5h", "h1"}
	for _, v := range invalid {
		t.Run("invalid/"+v, func(t *testing.T) {
			_, err := parseArgs("usage: test", []string{"ds", "q", "--start=" + v}, 2, promRangeSpecs()...)
			requireErrContains(t, err, "invalid --start value")
		})
	}
}

func TestParseArgsValidatesStepFlag(t *testing.T) {
	for _, v := range []string{"15s", "60s", "5m", "1h", "1h30m", "60"} {
		t.Run("valid/"+v, func(t *testing.T) {
			if _, err := parseArgs("usage: test", []string{"ds", "q", "--step", v}, 2, promRangeSpecs()...); err != nil {
				t.Fatalf("--step %s rejected: %v", v, err)
			}
		})
	}
	for _, v := range []string{"5minutes", "1 m", "m"} {
		t.Run("invalid/"+v, func(t *testing.T) {
			_, err := parseArgs("usage: test", []string{"ds", "q", "--step=" + v}, 2, promRangeSpecs()...)
			requireErrContains(t, err, "invalid --step value")
		})
	}
}

func TestParseArgsValidatesIntFlag(t *testing.T) {
	specs := []flagSpec{intFlag("--limit", "max lines")}

	t.Run("valid", func(t *testing.T) {
		p, err := parseArgs("usage: test", []string{"ds", "--limit", "50"}, 1, specs...)
		if err != nil {
			t.Fatalf("parseArgs: %v", err)
		}
		if got := p.intOr("--limit", defaultLimit); got != 50 {
			t.Fatalf("--limit = %d, want 50", got)
		}
	})

	t.Run("default when unset", func(t *testing.T) {
		p, err := parseArgs("usage: test", []string{"ds"}, 1, specs...)
		if err != nil {
			t.Fatalf("parseArgs: %v", err)
		}
		if got := p.intOr("--limit", defaultLimit); got != defaultLimit {
			t.Fatalf("--limit = %d, want %d", got, defaultLimit)
		}
	})

	// Previously strconv.Atoi's error was discarded, so `--limit abc` silently
	// became the default limit.
	for _, v := range []string{"abc", "0", "-1", "1.5", ""} {
		t.Run("invalid/"+v, func(t *testing.T) {
			_, err := parseArgs("usage: test", []string{"ds", "--limit=" + v}, 1, specs...)
			requireErrContains(t, err, "for --limit")
		})
	}
}

func TestParseArgsHelp(t *testing.T) {
	for _, arg := range []string{"-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			_, err := parseArgs("usage: test", []string{"ds", arg}, 2, promRangeSpecs()...)
			if !errors.Is(err, errHelp) {
				t.Fatalf("err = %v, want errHelp", err)
			}
		})
	}
}

func TestParseArgsErrorsIncludeUsage(t *testing.T) {
	const usage = "usage: grafana-cli prom query-range <datasource> <promql>"
	for _, args := range [][]string{
		{"ds"},                     // wrong positional count
		{"ds", "q", "--nope", "1"}, // unknown flag
		{"ds", "q", "--start"},     // missing value
	} {
		_, err := parseArgs(usage, args, 2, promRangeSpecs()...)
		requireErrContains(t, err, usage)
	}
}

// --- helpers ---

func mustParseArgs(t *testing.T, args []string, wantPos int) *parsedArgs {
	t.Helper()
	p, err := parseArgs("usage: test", args, wantPos, promRangeSpecs()...)
	if err != nil {
		t.Fatalf("parseArgs(%q): %v", args, err)
	}
	return p
}

func requireErrContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}
