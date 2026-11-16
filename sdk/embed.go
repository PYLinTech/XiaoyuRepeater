// Package sdk embeds the downloadable consumer SDK examples.
package sdk

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
)

// Version is the SDK package version exposed by the public version endpoint.
const Version = "1"

// Filename is the downloadable archive name.
const Filename = "XiaoyuRepeater_SDK_v" + Version + ".zip"

// archiveName is spelled out because go:embed requires a literal pattern.
const archiveName = "XiaoyuRepeater_SDK_v1.zip"

//go:generate go run ./gen -src files -out XiaoyuRepeater_SDK_v1.zip
//go:embed XiaoyuRepeater_SDK_v1.zip
var archiveSource embed.FS

type archiveConfig struct {
	BaseURL string `json:"baseUrl"`
}

// Archive builds the SDK zip with config.json pointing at baseURL.
func Archive(baseURL string) ([]byte, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("baseURL 必须以 http:// 或 https:// 开头")
	}
	config, err := json.MarshalIndent(archiveConfig{BaseURL: baseURL}, "", "  ")
	if err != nil {
		return nil, err
	}
	config = append(config, '\n')

	raw, err := archiveSource.ReadFile(archiveName)
	if err != nil {
		return nil, err
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	foundConfig := false
	for _, file := range reader.File {
		if err := checkEntryName(file.Name); err != nil {
			_ = writer.Close()
			return nil, err
		}
		data := []byte(nil)
		if file.Name == "config.json" {
			foundConfig = true
			data = config
		}
		if err := copyEntry(writer, file, data); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}
	if !foundConfig {
		_ = writer.Close()
		return nil, errors.New("SDK 包缺少 config.json")
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func copyEntry(writer *zip.Writer, file *zip.File, data []byte) error {
	if file.FileInfo().IsDir() {
		return nil
	}
	header := file.FileHeader
	target, err := writer.CreateHeader(&header)
	if err != nil {
		return err
	}
	if data != nil {
		_, err = target.Write(data)
		return err
	}
	source, err := file.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	_, err = io.Copy(target, source)
	return err
}

func checkEntryName(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return errors.New("SDK 包包含非法路径")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == "." {
			return errors.New("SDK 包包含非法路径")
		}
	}
	return nil
}
