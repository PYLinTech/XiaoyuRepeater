package sdk

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveContents(t *testing.T) {
	archive, err := Archive("https://relay.example.com/")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}

	entries := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		entries[file.Name] = file
		if file.Method != zip.Deflate {
			t.Errorf("%s: method=%d, want deflate", file.Name, file.Method)
		}
		if file.Modified.Year() != 1980 {
			t.Errorf("%s: modified=%s, want 1980 timestamp", file.Name, file.Modified)
		}
	}

	expected := []string{
		"README.md",
		"VERSION",
		"LICENSE",
		"config.json",
		"curl/README.md",
		"go/README.md",
		"go/go.mod",
		"go/go.sum",
		"go/types.go",
		"go/client.go",
		"go/agent.go",
		"go/cmd/consumer/main.go",
		"node/README.md",
		"node/package.json",
		"node/xiaoyurepeater-sdk-v1.mjs",
		"node/consumer.mjs",
		"python/README.md",
		"python/requirements.txt",
		"python/xiaoyurepeater_sdk_v1.py",
		"python/consumer.py",
	}
	for _, name := range expected {
		if entries[name] == nil {
			t.Errorf("missing archive entry %q", name)
		}
	}
	for name := range entries {
		if strings.Contains(name, "__pycache__") || strings.HasSuffix(name, ".pyc") || strings.HasSuffix(name, ".DS_Store") {
			t.Errorf("unexpected cache entry %q", name)
		}
		if strings.HasPrefix(name, "files/") {
			t.Errorf("entry %q should not keep the embed prefix", name)
		}
	}

	config := readEntry(t, entries["config.json"])
	var cfg archiveConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.BaseURL != "https://relay.example.com" {
		t.Fatalf("baseUrl=%q", cfg.BaseURL)
	}

	if version := strings.TrimSpace(string(readEntry(t, entries["VERSION"]))); version != Version {
		t.Fatalf("VERSION=%q, want %q", version, Version)
	}
	readme := string(readEntry(t, entries["README.md"]))
	if !strings.Contains(readme, "XiaoyuRepeater") {
		t.Fatal("README is missing the product name")
	}
	if !strings.Contains(readme, Filename) {
		t.Fatalf("README is missing %q", Filename)
	}
	license := string(readEntry(t, entries["LICENSE"]))
	if !strings.Contains(license, "MIT License") || !strings.Contains(license, "重庆沛雨霖科技有限公司") {
		t.Fatal("LICENSE is not the expected bilingual MIT license")
	}
}

func TestEmbeddedArchiveTracksSources(t *testing.T) {
	archive, err := Archive("https://relay.example.com")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	entries := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		entries[file.Name] = readEntry(t, file)
	}

	count := 0
	err = filepath.WalkDir("files", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel("files", name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skipSource(rel) || rel == "config.json" {
			return nil
		}
		count++
		source, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		packed, ok := entries[rel]
		if !ok {
			t.Errorf("%s is missing from %s; run go generate ./sdk", rel, Filename)
			return nil
		}
		if !bytes.Equal(source, packed) {
			t.Errorf("%s differs from %s; run go generate ./sdk", rel, Filename)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk files: %v", err)
	}
	if len(entries) != count+1 {
		t.Fatalf("%s has %d entries, sources have %d plus config.json", Filename, len(entries), count)
	}
}

func skipSource(rel string) bool {
	base := path.Base(rel)
	if base == ".DS_Store" || strings.HasSuffix(base, ".pyc") {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		switch part {
		case "node_modules", "__pycache__", ".pytest_cache", ".git":
			return true
		}
	}
	return false
}

func TestArchiveRejectsInvalidBaseURL(t *testing.T) {
	for _, value := range []string{"", "relay.example.com", "ftp://relay.example.com", "http://"} {
		if _, err := Archive(value); err == nil {
			t.Errorf("Archive(%q) succeeded, want error", value)
		}
	}
}

func readEntry(t *testing.T, file *zip.File) []byte {
	t.Helper()
	if file == nil {
		t.Fatal("missing zip entry")
	}
	reader, err := file.Open()
	if err != nil {
		t.Fatalf("open %s: %v", file.Name, err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read %s: %v", file.Name, err)
	}
	return data
}
