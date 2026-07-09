package identify

import (
	"context"
	"strings"
	"testing"
)

func TestGuessTitle_ParsesConfidentGuess(t *testing.T) {
	var seenPrompt string
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		seenPrompt = prompt
		return `{"title":"Breaking Bad (2008)"}`
	})
	defer closeSrv()

	title, err := GuessTitle(context.Background(), client, "brba.s01e01.720p-GROUP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if title != "Breaking Bad (2008)" {
		t.Fatalf("got %q", title)
	}
	if !strings.Contains(seenPrompt, "brba.s01e01.720p-GROUP") {
		t.Error("expected the original name to be embedded in the prompt")
	}
}

// The model has an explicit "I don't know" escape valve for opaque names —
// a null title must be treated as a real failure, not an empty-string guess
// that could go on to match an unrelated Lookup result.
func TestGuessTitle_DeclinesOnOpaqueName(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"title":null}`
	})
	defer closeSrv()

	_, err := GuessTitle(context.Background(), client, "xyz123")
	if err == nil {
		t.Fatal("expected an error when the model declines to guess")
	}
}

func TestGuessTitle_LiteralNullStringTreatedAsDecline(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `{"title":"null"}`
	})
	defer closeSrv()

	_, err := GuessTitle(context.Background(), client, "xyz123")
	if err == nil {
		t.Fatal("expected the literal string \"null\" to normalize to a decline")
	}
}

func TestGuessTitle_MalformedResponseErrors(t *testing.T) {
	client, closeSrv := fakeOllama(t, func(prompt string) string {
		return `not json`
	})
	defer closeSrv()

	_, err := GuessTitle(context.Background(), client, "xyz123")
	if err == nil {
		t.Fatal("expected an error for a malformed Ollama response")
	}
}
