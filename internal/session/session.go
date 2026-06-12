package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Aculnaj/aethercli/internal/config"
)

type Message struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

type Session struct {
	ID        string    `json:"id"`
	Model     string    `json:"model,omitempty"`
	Title     string    `json:"title,omitempty"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
	Messages  []Message `json:"messages"`
}

type Summary struct {
	ID        string
	Model     string
	Title     string
	UpdatedAt string
	Messages  int
}

type Store struct {
	dir string
	now func() time.Time
}

func NewStore(configPath string, now func() time.Time) (Store, error) {
	if now == nil {
		now = time.Now
	}
	if configPath == "" {
		defaultPath, err := config.DefaultPath()
		if err != nil {
			return Store{}, err
		}
		configPath = defaultPath
	}
	return Store{
		dir: filepath.Join(filepath.Dir(configPath), "sessions"),
		now: now,
	}, nil
}

func (s Store) New(model, title string) (Session, error) {
	now := s.now().UTC()
	id := now.Format("20060102-150405")
	for suffix := 2; ; suffix++ {
		if _, err := os.Stat(s.path(id)); errors.Is(err, os.ErrNotExist) {
			break
		}
		id = fmt.Sprintf("%s-%d", now.Format("20060102-150405"), suffix)
	}
	stamp := now.Format(time.RFC3339)
	return Session{
		ID:        id,
		Model:     model,
		Title:     titleFromPrompt(title),
		CreatedAt: stamp,
		UpdatedAt: stamp,
	}, nil
}

func (s Store) Load(id string) (Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Session{}, fmt.Errorf("missing session id")
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return Session{}, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s Store) Latest() (Session, error) {
	summaries, err := s.List()
	if err != nil {
		return Session{}, err
	}
	if len(summaries) == 0 {
		return Session{}, fmt.Errorf("no saved sessions")
	}
	return s.Load(summaries[0].ID)
}

func (s Store) Save(session Session) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	session.UpdatedAt = s.now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(s.path(session.ID), data, 0o600)
}

func (s Store) Append(session *Session, role, content string) {
	session.Messages = append(session.Messages, Message{
		Role:      role,
		Content:   content,
		CreatedAt: s.now().UTC().Format(time.RFC3339),
	})
}

func (s Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var summaries []Summary
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		item, err := s.Load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, Summary{
			ID:        item.ID,
			Model:     item.Model,
			Title:     item.Title,
			UpdatedAt: item.UpdatedAt,
			Messages:  len(item.Messages),
		})
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	return summaries, nil
}

func (s Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

func titleFromPrompt(prompt string) string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	runes := []rune(prompt)
	if len(runes) <= 60 {
		return prompt
	}
	return string(runes[:57]) + "..."
}
