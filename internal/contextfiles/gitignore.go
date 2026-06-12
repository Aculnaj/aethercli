package contextfiles

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

type ignoreRule struct {
	pattern  string
	negated  bool
	dirOnly  bool
	anchored bool
}

type ignoreMatcher struct {
	rules []ignoreRule
}

func loadIgnore(root string) ignoreMatcher {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return ignoreMatcher{}
	}

	var rules []ignoreRule
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.negated = true
			line = strings.TrimPrefix(line, "!")
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if strings.HasPrefix(line, "/") {
			rule.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		if line != "" {
			rule.pattern = path.Clean(filepath.ToSlash(line))
			rules = append(rules, rule)
		}
	}
	return ignoreMatcher{rules: rules}
}

func (m ignoreMatcher) Ignored(rel string, isDir bool) bool {
	rel = path.Clean(filepath.ToSlash(rel))
	ignored := false
	for _, rule := range m.rules {
		if rule.dirOnly && !isDir {
			continue
		}
		if matchesRule(rule, rel) {
			ignored = !rule.negated
		}
	}
	return ignored
}

func matchesRule(rule ignoreRule, rel string) bool {
	pattern := rule.pattern
	if rule.anchored || strings.Contains(pattern, "/") {
		return matchPath(pattern, rel) || strings.HasPrefix(rel, pattern+"/")
	}

	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if matchPath(pattern, part) {
			return true
		}
	}
	return false
}

func matchPath(pattern, value string) bool {
	ok, err := path.Match(pattern, value)
	if err == nil && ok {
		return true
	}
	return pattern == value
}
