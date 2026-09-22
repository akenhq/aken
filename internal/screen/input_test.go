// SPDX-License-Identifier: Apache-2.0
package screen

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// keyInput returns an Input that reads one key at a time without a terminal.
func keyInput(t *testing.T, text string) *Input {
	t.Helper()
	original := makeRaw
	makeRaw = func(int) (func(), error) { return func() {}, nil }
	t.Cleanup(func() { makeRaw = original })
	in := NewInput(strings.NewReader(text), -1, true)
	if !in.keys || !in.Interactive() {
		t.Fatal("expected one key at a time")
	}
	return in
}

func TestKeyOnTerminal(t *testing.T) {
	// Arrow keys, function keys and other controls must not answer a prompt.
	in := keyInput(t, "a \r\n\x1b[A\x1bOB\x1b[1;5D\tcafé")
	for _, want := range []rune{'a', ' ', '\n', '\n', 0, 0, 0, 0, 'c', 'a', 'f', 'é'} {
		got, err := in.Key()
		if err != nil || got != want {
			t.Fatalf("key = %q, %v, want %q", got, err, want)
		}
	}
	if _, err := in.Key(); !errors.Is(err, io.EOF) {
		t.Fatalf("end of input = %v", err)
	}
	for _, tt := range []struct {
		text string
		err  error
	}{{"\x03", ErrInterrupted}, {"\x04", io.EOF}} {
		if _, err := keyInput(t, tt.text).Key(); !errors.Is(err, tt.err) {
			t.Fatalf("%q = %v, want %v", tt.text, err, tt.err)
		}
	}
}

func TestKeyWithoutTerminal(t *testing.T) {
	in := NewInput(strings.NewReader("a\n\n  d  \nno\n"), -1, false)
	if in.Interactive() {
		t.Fatal("a pipe has nobody to ask")
	}
	for _, want := range []rune{'a', '\n', 'd', 0} {
		got, err := in.Key()
		if err != nil || got != want {
			t.Fatalf("key = %q, %v, want %q", got, err, want)
		}
	}
	if _, err := in.Key(); !errors.Is(err, io.EOF) {
		t.Fatalf("end of input = %v", err)
	}
}

func TestKeyIsNotRawWithoutATerminal(t *testing.T) {
	// A descriptor that will not go raw reads whole lines rather than failing.
	in := NewInput(strings.NewReader("a\n"), -1, true)
	if in.keys || !in.Interactive() {
		t.Fatal("expected the whole-line fallback")
	}
	if key, err := in.Key(); key != 'a' || err != nil {
		t.Fatalf("key = %q, %v", key, err)
	}
}

func TestAsk(t *testing.T) {
	choices := []Choice{{Key: 's', Label: "send"}, {Key: 'a', Label: "abort"}}
	var out bytes.Buffer
	Choices(&out, choices)
	if out.String() != "[s] send   [a] abort\n" {
		t.Fatalf("legend = %q", out.String())
	}

	// On a terminal the key acts as it is pressed and the answer is echoed;
	// keys that answer nothing here are ignored.
	out.Reset()
	if key, err := Ask(keyInput(t, "xqa"), &out, choices); key != 'a' || err != nil {
		t.Fatalf("key = %q, %v", key, err)
	}
	if out.String() != "> abort\n" {
		t.Fatalf("prompt = %q", out.String())
	}

	// A pipe answers in whole lines and is prompted again for each one.
	out.Reset()
	if key, err := Ask(NewInput(strings.NewReader("x\ns\n"), -1, false), &out, choices); key != 's' || err != nil {
		t.Fatalf("key = %q, %v", key, err)
	}
	if out.String() != ">\n>\n" {
		t.Fatalf("prompt = %q", out.String())
	}

	out.Reset()
	if _, err := Ask(keyInput(t, "\x03"), &out, choices); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("interrupt = %v", err)
	}
	if out.String() != ">\n" {
		t.Fatalf("interrupted prompt = %q", out.String())
	}
}

func TestAskDropsKeysTypedBeforeThePrompt(t *testing.T) {
	// Whatever was typed before the question appeared must not answer it.
	in := keyInput(t, "xsa")
	if key, err := in.Key(); key != 'x' || err != nil {
		t.Fatalf("key = %q, %v", key, err)
	}
	var out bytes.Buffer
	if _, err := Ask(in, &out, []Choice{{Key: 's', Label: "send"}}); !errors.Is(err, io.EOF) {
		t.Fatalf("pending keys answered the prompt: %v, %q", err, out.String())
	}
}

func TestUntilStopsWithTheContext(t *testing.T) {
	// A key that is never pressed must not hold a session open.
	ctx, cancel := context.WithCancelCause(context.Background())
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	original := makeRaw
	makeRaw = func(int) (func(), error) { return func() {}, nil }
	t.Cleanup(func() { makeRaw = original })
	in := NewInput(reader, -1, true).Until(ctx)
	if !in.keys || !in.Interactive() {
		t.Fatal("Until forgot the terminal")
	}
	stop := errors.New("session ended")
	cancel(stop)
	if _, err := in.Key(); !errors.Is(err, stop) {
		t.Fatalf("read outlived the session: %v", err)
	}
}
