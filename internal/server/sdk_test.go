package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"xiaoyurepeater/internal/config"
	"xiaoyurepeater/internal/protocol"
	"xiaoyurepeater/sdk"
)

func TestSDKVersionIsPublic(t *testing.T) {
	env := newTestEnv(t, nil)
	response, err := env.ts.Client().Get(env.ts.URL + "/api/sdk/version")
	if err != nil {
		t.Fatalf("GET version: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	var body struct {
		Name         string   `json:"name"`
		Version      string   `json:"version"`
		Protocol     int      `json:"protocol"`
		Filename     string   `json:"filename"`
		DownloadPath string   `json:"download_path"`
		Languages    []string `json:"languages"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Version != sdk.Version || body.Protocol != protocol.Version {
		t.Fatalf("version=%q protocol=%d", body.Version, body.Protocol)
	}
	if body.Filename != sdk.Filename || body.DownloadPath != "/api/sdk/download" {
		t.Fatalf("filename=%q download_path=%q", body.Filename, body.DownloadPath)
	}
	if body.Name != "XiaoyuRepeater SDK" {
		t.Fatalf("name=%q", body.Name)
	}
	if strings.Join(body.Languages, ",") != "go,node,python,curl" {
		t.Fatalf("languages=%v", body.Languages)
	}
}

func TestSDKDownloadInjectsRequestOrigin(t *testing.T) {
	env := newTestEnv(t, nil)
	response, err := env.ts.Client().Get(env.ts.URL + "/api/sdk/download")
	if err != nil {
		t.Fatalf("GET download: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "application/zip" {
		t.Fatalf("content-type=%q", contentType)
	}
	if disposition := response.Header.Get("Content-Disposition"); !strings.Contains(disposition, sdk.Filename) {
		t.Fatalf("content-disposition=%q", disposition)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control=%q", response.Header.Get("Cache-Control"))
	}
	if response.Header.Get("X-SDK-Version") != sdk.Version {
		t.Fatalf("x-sdk-version=%q", response.Header.Get("X-SDK-Version"))
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if response.ContentLength >= 0 && int64(len(raw)) != response.ContentLength {
		t.Fatalf("content-length=%d body=%d", response.ContentLength, len(raw))
	}

	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	var configFile *zip.File
	for _, file := range reader.File {
		if file.Name == "config.json" {
			configFile = file
			break
		}
	}
	if configFile == nil {
		t.Fatal("archive is missing config.json")
	}
	configReader, err := configFile.Open()
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	defer configReader.Close()
	var cfg struct {
		BaseURL string `json:"baseUrl"`
	}
	if err := json.NewDecoder(configReader).Decode(&cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.BaseURL != env.ts.URL {
		t.Fatalf("baseUrl=%q, want %q", cfg.BaseURL, env.ts.URL)
	}
}

func TestSDKDownloadHonorsForwardedHTTPS(t *testing.T) {
	env := newTestEnv(t, func(cfg *config.Config) { cfg.TrustProxy = true })
	request, err := http.NewRequest(http.MethodGet, env.ts.URL+"/api/sdk/download", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("X-Forwarded-Proto", "https")
	response, err := env.ts.Client().Do(request)
	if err != nil {
		t.Fatalf("GET download: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	for _, file := range reader.File {
		if file.Name != "config.json" {
			continue
		}
		entry, err := file.Open()
		if err != nil {
			t.Fatalf("open config: %v", err)
		}
		defer entry.Close()
		var cfg struct {
			BaseURL string `json:"baseUrl"`
		}
		if err := json.NewDecoder(entry).Decode(&cfg); err != nil {
			t.Fatalf("decode config: %v", err)
		}
		want := "https://" + strings.TrimPrefix(env.ts.URL, "http://")
		if cfg.BaseURL != want {
			t.Fatalf("baseUrl=%q, want %q", cfg.BaseURL, want)
		}
		return
	}
	t.Fatal("archive is missing config.json")
}
