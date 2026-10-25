// Package tty answers whether spinclass is talking to a terminal: the one
// isatty check the prompting paths (close, merge, clean, resume, the session
// picker, shop's detach) share, instead of a copy in each (#230).
package tty

import (
	"os"

	"github.com/mattn/go-isatty"
)

// Terminal reports whether f is a terminal, counting a Cygwin/MSYS pty as one.
func Terminal(f *os.File) bool {
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// Interactive reports whether both stdin and stderr are terminals: huh renders
// its prompts on stderr (tea.WithOutput(os.Stderr)), so a prompt needs both.
func Interactive() bool {
	return Terminal(os.Stdin) && Terminal(os.Stderr)
}
