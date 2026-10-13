package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// prompter asks questions on the command line. Without a terminal, it
// reads one answer per line from stdin, so scripts can pipe answers in.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	// tty is stdin when it's a terminal, so secrets can be typed or pasted
	// without being shown.
	tty *os.File
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	p := &prompter{in: bufio.NewReader(in), out: out}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.tty = f
	}
	return p
}

// terminal reports whether someone is there to answer.
func (p *prompter) terminal() bool { return p.tty != nil }

// secret asks for a secret. On a terminal, what you type or paste isn't
// shown, so it never ends up on screen or in your scrollback.
func (p *prompter) secret(question string) (string, error) {
	fmt.Fprint(p.out, question)
	if p.tty == nil {
		return p.line()
	}
	b, err := term.ReadPassword(int(p.tty.Fd()))
	fmt.Fprintln(p.out)
	return strings.TrimSpace(string(b)), err
}

// yes asks a yes-or-no question. An empty answer is def.
func (p *prompter) yes(question string, def bool) (bool, error) {
	fmt.Fprint(p.out, question)
	answer, err := p.line()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// line reads one line. Running out of input is io.EOF, unless the last
// line just has no newline.
func (p *prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if p.tty == nil {
		fmt.Fprintln(p.out) // nothing was echoed, so end the question's line
	}
	if err != nil && (err != io.EOF || s == "") {
		return "", err
	}
	return strings.TrimSpace(s), nil
}
