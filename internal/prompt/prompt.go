package prompt

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

var ErrEmptyPrompt = errors.New("prompt is empty")

type Options struct {
	Args         []string
	Stdin        io.Reader
	StdinHasData bool
	Ask          func(label string) (string, error)
}

func Resolve(opts Options) (string, error) {
	if len(opts.Args) > 0 {
		return clean(strings.Join(opts.Args, " "))
	}

	if opts.StdinHasData && opts.Stdin != nil {
		data, err := io.ReadAll(opts.Stdin)
		if err != nil {
			return "", err
		}
		return clean(string(data))
	}

	if opts.Ask != nil {
		value, err := opts.Ask("Prompt")
		if err != nil {
			return "", err
		}
		return clean(value)
	}

	return "", fmt.Errorf("%w: pass a prompt argument, pipe stdin, or run interactively", ErrEmptyPrompt)
}

func clean(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrEmptyPrompt
	}
	return value, nil
}
