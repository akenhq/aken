// SPDX-License-Identifier: Apache-2.0
package screen

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// ErrInterrupted reports Ctrl-C at a prompt. A terminal in raw mode delivers
// the byte instead of raising SIGINT, so prompts turn it back into an error.
var ErrInterrupted = errors.New("interrupted")

// makeRaw switches the terminal to raw mode for one read; replaced in tests.
var makeRaw = func(fd int) (func(), error) {
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() { _ = term.Restore(fd, state) }, nil
}

// Choice is one key a prompt accepts and the action that key takes.
type Choice struct {
	Key   rune
	Label string
}

// Input reads the answers to prompts. On a terminal a key acts the moment it
// is pressed; on anything else a whole line is read, so pipes, scripts and
// tests keep working.
type Input struct {
	source   io.Reader
	reader   *bufio.Reader
	fd       int
	terminal bool // a person is answering
	keys     bool // and the terminal hands over one key at a time
}

// NewInput reads answers from r. fd is the descriptor behind r and terminal
// says whether a person is answering there.
func NewInput(r io.Reader, fd int, terminal bool) *Input {
	in := &Input{source: r, reader: bufio.NewReader(r), fd: fd, terminal: terminal}
	// One key at a time needs raw mode. Ask for it once here, not at a prompt,
	// so a descriptor that will not take it reads whole lines from the start.
	if terminal {
		if restore, err := makeRaw(fd); err == nil {
			restore()
			in.keys = true
		}
	}
	return in
}

// Interactive reports whether a person is answering the prompts.
func (in *Input) Interactive() bool { return in.terminal }

// Until returns an Input over the same source that stops reading as soon as
// ctx is done, so a key that is never pressed cannot outlive a session. Call
// it before the first read and use the result from then on: anything the
// original has buffered stays with the original.
func (in *Input) Until(ctx context.Context) *Input {
	next := contextReader{ctx, in.source}
	return &Input{source: next, reader: bufio.NewReader(next), fd: in.fd, terminal: in.terminal, keys: in.keys}
}

// A terminal read must not prevent expiry or a relay deletion from ending the session.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	type readResult struct {
		data []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() { data := make([]byte, len(p)); n, err := r.reader.Read(data); done <- readResult{data[:n], err} }()
	select {
	case <-r.ctx.Done():
		return 0, context.Cause(r.ctx)
	case result := <-done:
		return copy(p, result.data), result.err
	}
}

// Key reads one answer. On a terminal it returns as soon as a key is pressed,
// without echoing it; otherwise it reads a line and returns its single rune.
// Enter is '\n'; a key with no meaning here, an escape sequence such as an
// arrow key, and a line of more than one rune are all 0. Ctrl-C is
// ErrInterrupted and Ctrl-D is io.EOF, the errors those keys raise when the
// terminal handles them itself.
func (in *Input) Key() (rune, error) {
	if !in.keys {
		line, err := in.reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		text := strings.TrimSpace(line)
		if text == "" {
			return '\n', nil
		}
		if r, size := utf8.DecodeRuneInString(text); size == len(text) {
			return r, nil
		}
		return 0, nil
	}
	restore, err := makeRaw(in.fd)
	if err != nil {
		return 0, err
	}
	defer restore()
	r, _, err := in.reader.ReadRune()
	if err != nil {
		return 0, err
	}
	switch {
	case r == 0x03:
		return 0, ErrInterrupted
	case r == 0x04:
		return 0, io.EOF
	case r == '\r' || r == '\n':
		return '\n', nil
	case r == 0x1b:
		in.escape()
		return 0, nil
	case r < 0x20 || r == 0x7f:
		return 0, nil
	}
	return r, nil
}

// escape swallows the rest of an arrow or function key sequence so that its
// letters never answer a prompt. Terminals send the whole sequence at once, so
// only what is already buffered belongs to it.
func (in *Input) escape() {
	if in.reader.Buffered() == 0 {
		return
	}
	b, err := in.reader.ReadByte()
	if err != nil || b != '[' && b != 'O' {
		return
	}
	for in.reader.Buffered() > 0 {
		if b, err := in.reader.ReadByte(); err != nil || b >= 0x40 && b <= 0x7e {
			return
		}
	}
}

// Choices writes the key legend, for example
// "[s] send   [v] view everything   [a] abort".
func Choices(w io.Writer, choices []Choice) {
	keys := make([]string, len(choices))
	for i, c := range choices {
		keys[i] = fmt.Sprintf("[%c] %s", c.Key, c.Label)
	}
	_, _ = fmt.Fprintln(w, strings.Join(keys, "   "))
}

// Ask writes the prompt marker and returns the key that was chosen. Keys
// outside choices do nothing: a screen must not be answered by accident.
// Whatever was read ahead of the prompt is dropped for the same reason, so a
// paste or a held key that ran past a listing cannot answer the question
// after it. Keys still queued in the terminal are not ours to drop, so this
// is not a guarantee that every answer was deliberate.
func Ask(in *Input, w io.Writer, choices []Choice) (rune, error) {
	if in.keys {
		_, _ = in.reader.Discard(in.reader.Buffered())
	}
	_, _ = fmt.Fprint(w, ">")
	if !in.keys {
		_, _ = fmt.Fprint(w, "\n")
	}
	for {
		key, err := in.Key()
		if err != nil {
			if in.keys {
				_, _ = fmt.Fprintln(w)
			}
			return 0, err
		}
		for _, c := range choices {
			if c.Key == key {
				if in.keys {
					_, _ = fmt.Fprintf(w, " %s\n", c.Label)
				}
				return key, nil
			}
		}
		if !in.keys {
			_, _ = fmt.Fprint(w, ">\n")
		}
	}
}
