package prompt

import (
	"strings"
	"testing"
)

func TestResolveUsesPromptArguments(t *testing.T) {
	got, err := Resolve(Options{
		Args:         []string{"hello", "world"},
		Stdin:        strings.NewReader("ignored"),
		StdinHasData: true,
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("Resolve = %q, want joined args", got)
	}
}

func TestResolveReadsStdinWhenNoArguments(t *testing.T) {
	got, err := Resolve(Options{
		Stdin:        strings.NewReader("long prompt\nfrom pipe\n"),
		StdinHasData: true,
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != "long prompt\nfrom pipe" {
		t.Fatalf("Resolve = %q, want trimmed stdin prompt", got)
	}
}

func TestResolveAsksInteractivelyWhenNoInput(t *testing.T) {
	got, err := Resolve(Options{
		Ask: func(label string) (string, error) {
			if label != "Prompt" {
				t.Fatalf("interactive label = %q, want Prompt", label)
			}
			return "interactive prompt", nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != "interactive prompt" {
		t.Fatalf("Resolve = %q, want interactive prompt", got)
	}
}

func TestResolveRejectsEmptyPrompt(t *testing.T) {
	_, err := Resolve(Options{
		Args: []string{"   "},
	})
	if err == nil {
		t.Fatal("Resolve returned nil error for empty prompt")
	}
}
