package mcp

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

//go:embed templates/*
var templatesFS embed.FS

// TemplateListing is the schema for list_templates tool.
type TemplateListing struct {
	Templates []TemplateInfo `json:"templates"`
}

// TemplateInfo represents a single template.
type TemplateInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Files       []string `json:"files"`
}

// listTemplates reads the templates directory and returns a JSON list of available templates.
func listTemplates() (string, error) {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		return "", err
	}

	var listing TemplateListing
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		var files []string
		_ = fs.WalkDir(templatesFS, filepath.Join("templates", name), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				rel, _ := filepath.Rel(filepath.Join("templates", name), path)
				files = append(files, rel)
			}
			return nil
		})

		sort.Strings(files)

		listing.Templates = append(listing.Templates, TemplateInfo{
			Name:  name,
			Files: files,
		})
	}

	sort.Slice(listing.Templates, func(i, j int) bool {
		return listing.Templates[i].Name < listing.Templates[j].Name
	})

	b, err := json.Marshal(listing)
	if err != nil {
		return "", err
	}

	return string(b), nil
}

// CopyTemplate copies an embedded template (canary or loan) into destDir.
func CopyTemplate(name, destDir string) error {
	switch name {
	case "canary":
		name = "canary-rollout"
	case "loan":
		name = "loan-underwriting"
	}
	srcDir := filepath.Join("templates", name)
	if _, err := fs.Stat(templatesFS, srcDir); err != nil {
		return fmt.Errorf("unknown template %q (available: canary, loan)", name)
	}

	return fs.WalkDir(templatesFS, srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(destDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(templatesFS, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
