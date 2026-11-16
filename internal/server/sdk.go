package server

import (
	"fmt"
	"net/http"
	"strconv"

	"xiaoyurepeater/internal/protocol"
	"xiaoyurepeater/sdk"
)

func (s *Server) handleSDKVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":          "XiaoyuRepeater SDK",
		"version":       sdk.Version,
		"protocol":      protocol.Version,
		"filename":      sdk.Filename,
		"download_path": "/api/sdk/download",
		"languages":     []string{"go", "node", "python", "curl"},
	})
}

func (s *Server) handleSDKDownload(w http.ResponseWriter, r *http.Request) {
	baseURL := "http://" + r.Host
	if s.isHTTPS(r) {
		baseURL = "https://" + r.Host
	}
	archive, err := sdk.Archive(baseURL)
	if err != nil {
		s.log.Error("生成 SDK 包失败", "error", err, "host", r.Host)
		writeError(w, http.StatusInternalServerError, "生成 SDK 包失败")
		return
	}
	header := w.Header()
	header.Set("Content-Type", "application/zip")
	header.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sdk.Filename))
	header.Set("Content-Length", strconv.Itoa(len(archive)))
	header.Set("Cache-Control", "no-store")
	header.Set("X-SDK-Version", sdk.Version)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(archive); err != nil {
		s.log.Warn("发送 SDK 包失败", "error", err)
	}
}
