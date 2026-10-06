package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// checkPositionals validates the arguments left after flag parsing. Go's flag
// package stops at the first non-flag argument, so anything after it is never
// parsed: `tirion index /repos -skip-unchanged=false` would silently drop the
// flag. max < 0 means any number of positionals is allowed (free-text queries);
// in that case only a known flag placed after the first positional is rejected.
func checkPositionals(fs *flag.FlagSet, command string, min, max int) error {
	args := fs.Args()
	if len(args) < min {
		return fmt.Errorf("%s: expected %s, got %d argument(s)", command, plural(min, "argument"), len(args))
	}
	if max < 0 {
		for _, arg := range args[1:] {
			if name, ok := knownFlagName(fs, arg); ok {
				return fmt.Errorf("%s: flag -%s appears after the positional argument %q and would be ignored; put every flag before the positional arguments", command, name, args[0])
			}
		}
		return nil
	}
	if len(args) <= max {
		return nil
	}
	extra := args[max:]
	message := fmt.Sprintf("%s: unexpected argument(s) %q", command, extra)
	if max == 0 {
		message += "; this command takes no positional arguments"
	} else {
		message += fmt.Sprintf("; it takes at most %s", plural(max, "positional argument"))
	}
	for _, arg := range extra {
		if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			message += "; flags must come before positional arguments, otherwise they are ignored"
			break
		}
	}
	return fmt.Errorf("%s", message)
}

func knownFlagName(fs *flag.FlagSet, arg string) (string, bool) {
	if !strings.HasPrefix(arg, "-") || len(arg) < 2 {
		return "", false
	}
	name := strings.TrimLeft(arg, "-")
	if i := strings.Index(name, "="); i >= 0 {
		name = name[:i]
	}
	if name == "" || fs.Lookup(name) == nil {
		return "", false
	}
	return name, true
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// mustPositionals exits with a usage error (status 2, like a bad flag) when the
// positional arguments are not acceptable.
func mustPositionals(fs *flag.FlagSet, command string, min, max int) {
	if err := checkPositionals(fs, command, min, max); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n", err)
		if fs.Usage != nil {
			fs.Usage()
		}
		os.Exit(2)
	}
}
