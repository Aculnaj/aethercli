package contextfiles

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const DefaultMaxBytes = 200_000

type Options struct {
	Files    []string
	Dirs     []string
	MaxBytes int
}

type Document struct {
	Path    string
	Content string
}

func Collect(opts Options) ([]Document, error) {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	var docs []Document
	totalBytes := 0
	for _, path := range opts.Files {
		doc, ok, err := readDocument(path, path)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		totalBytes += len(doc.Content)
		if totalBytes > maxBytes {
			return nil, fmt.Errorf("context exceeds %d bytes; pass fewer files or a smaller directory", maxBytes)
		}
		docs = append(docs, doc)
	}

	for _, dir := range opts.Dirs {
		collected, err := collectDir(dir)
		if err != nil {
			return nil, err
		}
		for _, doc := range collected {
			totalBytes += len(doc.Content)
			if totalBytes > maxBytes {
				return nil, fmt.Errorf("context exceeds %d bytes; pass fewer files or a smaller directory", maxBytes)
			}
			docs = append(docs, doc)
		}
	}

	sort.SliceStable(docs, func(i, j int) bool {
		return docs[i].Path < docs[j].Path
	})
	return docs, nil
}

func BuildPrompt(base string, docs []Document) string {
	if len(docs) == 0 {
		return base
	}

	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n<context>\n")
	for _, doc := range docs {
		b.WriteString(`<file path="`)
		b.WriteString(escapeAttr(doc.Path))
		b.WriteString("\">\n")
		b.WriteString(doc.Content)
		if !strings.HasSuffix(doc.Content, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("</file>\n")
	}
	b.WriteString("</context>")
	return b.String()
}

func collectDir(root string) ([]Document, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("context path %q is not a directory", root)
	}

	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	ignore := loadIgnore(root)
	var docs []Document
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if shouldSkipBuiltin(rel, entry.IsDir()) || ignore.Ignored(rel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		doc, ok, err := readDocument(path, rel)
		if err != nil {
			return err
		}
		if ok {
			docs = append(docs, doc)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return docs, nil
}

func readDocument(path, label string) (Document, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, false, err
	}
	if bytes.Contains(data, []byte{0}) {
		return Document{}, false, nil
	}
	return Document{Path: filepath.ToSlash(label), Content: string(data)}, true, nil
}

func shouldSkipBuiltin(rel string, isDir bool) bool {
	parts := strings.Split(rel, "/")
	name := parts[len(parts)-1]
	switch name {
	case ".git", ".context", "node_modules", "vendor", "dist", "build", ".DS_Store":
		return true
	}
	if !isDir && strings.HasSuffix(name, ".lock") {
		return true
	}
	return false
}

func escapeAttr(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	return value
}
