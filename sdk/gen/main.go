// Command gen builds the deterministic SDK zip embedded by the relay.
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	src := flag.String("src", "files", "SDK source directory")
	out := flag.String("out", "XiaoyuRepeater_SDK_v1.zip", "output archive")
	flag.Parse()
	if err := build(*src, *out); err != nil {
		log.Fatal(err)
	}
}

func build(src, out string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s 不是目录", src)
	}
	outAbs, err := filepath.Abs(out)
	if err != nil {
		return err
	}

	temp, err := os.CreateTemp(filepath.Dir(outAbs), ".sdk-*.zip")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	writer := zip.NewWriter(temp)
	walkErr := filepath.WalkDir(src, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		abs, err := filepath.Abs(name)
		if err != nil {
			return err
		}
		if abs == outAbs {
			return nil
		}
		rel, err := filepath.Rel(src, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || skip(rel) || !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{
			Name:     rel,
			Method:   zip.Deflate,
			Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		header.SetMode(0o644)
		target, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = target.Write(data)
		return err
	})
	if walkErr != nil {
		_ = writer.Close()
		_ = temp.Close()
		return walkErr
	}
	if err := writer.Close(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempName, 0o644); err != nil {
		return err
	}
	return os.Rename(tempName, outAbs)
}

func skip(rel string) bool {
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
