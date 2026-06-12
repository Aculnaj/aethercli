package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/Aculnaj/aethercli/internal/config"
	"github.com/Aculnaj/aethercli/internal/contextfiles"
	"github.com/Aculnaj/aethercli/internal/prompt"
	"github.com/Aculnaj/aethercli/internal/session"
)

type chatOptions struct {
	model       string
	resume      bool
	sessionID   string
	files       []string
	contextDirs []string
}

func newChatCommand(deps Deps) *cobra.Command {
	opts := chatOptions{}
	cmd := &cobra.Command{
		Use:   "chat [prompt]",
		Short: "Send a prompt with saved conversation history",
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(deps.In)
			cfg, apiKey, err := ensureConfigured(deps, reader, true)
			if err != nil {
				return err
			}
			resolvedPrompt, err := prompt.Resolve(prompt.Options{
				Args:         args,
				Stdin:        deps.In,
				StdinHasData: deps.StdinHasData(),
				Ask:          lineAsker(deps.Err, reader),
			})
			if err != nil {
				return err
			}

			model := strings.TrimSpace(opts.model)
			if model == "" {
				model = strings.TrimSpace(cfg.DefaultModel)
			}
			if model == "" {
				return fmt.Errorf("missing model: pass --model or run `aether setup`")
			}
			return runChat(cmd.Context(), deps, cfg, apiKey, model, resolvedPrompt, opts)
		},
	}
	cmd.Flags().StringVarP(&opts.model, "model", "m", "", "chat model ID")
	cmd.Flags().BoolVar(&opts.resume, "resume", false, "resume the latest saved session")
	cmd.Flags().StringVar(&opts.sessionID, "session", "", "resume a specific session ID")
	cmd.Flags().StringArrayVarP(&opts.files, "file", "f", nil, "include a file as prompt context")
	cmd.Flags().StringArrayVar(&opts.contextDirs, "context", nil, "include a directory as prompt context")
	return cmd
}

func newSessionsCommand(deps Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Manage saved chat sessions",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List saved chat sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := session.NewStore(deps.ConfigPath, deps.Now)
			if err != nil {
				return err
			}
			summaries, err := store.List()
			if err != nil {
				return err
			}
			return printSessionSummaries(deps.Out, summaries)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show [session-id]",
		Short: "Print a saved chat session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := session.NewStore(deps.ConfigPath, deps.Now)
			if err != nil {
				return err
			}
			item, err := store.Load(args[0])
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(deps.Out)
			encoder.SetIndent("", "  ")
			return encoder.Encode(item)
		},
	})
	return cmd
}

func runChat(ctx context.Context, deps Deps, cfg config.Config, apiKey, model, userPrompt string, opts chatOptions) error {
	store, err := session.NewStore(deps.ConfigPath, deps.Now)
	if err != nil {
		return err
	}

	item, err := loadOrCreateChatSession(store, model, userPrompt, opts)
	if err != nil {
		return err
	}

	promptWithFiles, err := addPromptContext(userPrompt, opts.files, opts.contextDirs)
	if err != nil {
		return err
	}
	requestPrompt := conversationPrompt(item.Messages, promptWithFiles)
	client := deps.ClientFactory(cfg.BaseURL, apiKey)
	stopThinking := startThinkingSpinner(deps.Err, thinkingSpinnerInterval)
	resp, err := client.Chat(ctx, api.ChatRequest{
		Model:  model,
		Prompt: requestPrompt,
	})
	stopThinking()
	if err != nil {
		return userFacingError(err)
	}

	store.Append(&item, "user", userPrompt)
	store.Append(&item, "assistant", resp.Content)
	item.Model = model
	if item.Title == "" {
		item.Title = userPrompt
	}
	if err := store.Save(item); err != nil {
		return err
	}
	_, err = fmt.Fprintln(deps.Out, resp.Content)
	return err
}

func loadOrCreateChatSession(store session.Store, model, userPrompt string, opts chatOptions) (session.Session, error) {
	if strings.TrimSpace(opts.sessionID) != "" {
		return store.Load(opts.sessionID)
	}
	if opts.resume {
		return store.Latest()
	}
	return store.New(model, userPrompt)
}

func conversationPrompt(messages []session.Message, currentPrompt string) string {
	if len(messages) == 0 {
		return currentPrompt
	}

	var b strings.Builder
	b.WriteString("Continue the conversation using the transcript below.\n\nTranscript:\n")
	for _, message := range messages {
		switch message.Role {
		case "user":
			b.WriteString("User: ")
		case "assistant":
			b.WriteString("Assistant: ")
		default:
			b.WriteString(roleLabel(message.Role))
			b.WriteString(": ")
		}
		b.WriteString(message.Content)
		if !strings.HasSuffix(message.Content, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString("\nCurrent user message:\nUser: ")
	b.WriteString(currentPrompt)
	return b.String()
}

func roleLabel(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return "Message"
	}
	return strings.ToUpper(role[:1]) + role[1:]
}

func addPromptContext(base string, files, dirs []string) (string, error) {
	if len(files) == 0 && len(dirs) == 0 {
		return base, nil
	}
	docs, err := contextfiles.Collect(contextfiles.Options{
		Files: files,
		Dirs:  dirs,
	})
	if err != nil {
		return "", err
	}
	return contextfiles.BuildPrompt(base, docs), nil
}

func printSessionSummaries(out io.Writer, summaries []session.Summary) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ID\tUPDATED\tMODEL\tMESSAGES\tTITLE"); err != nil {
		return err
	}
	for _, item := range summaries {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%s\n", item.ID, item.UpdatedAt, item.Model, item.Messages, item.Title); err != nil {
			return err
		}
	}
	return writer.Flush()
}
