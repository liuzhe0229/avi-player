package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"avi-player/internal/store"
	"avi-player/internal/transcode"
)

type Server struct {
	addr string

	dataDir    string
	uploadsDir string
	hlsDir     string

	ffmpegPath string

	store *store.Store

	// Limit concurrent transcodes (CPU-heavy)
	transcodeSem chan struct{}

	// Cleanup policy
	maxJobs int
	jobTTL  time.Duration
}

func main() {
	addr := envString("ADDR", "127.0.0.1:8080")
	dataDir := envString("DATA_DIR", "./data")
	ffmpegPath := resolveFFmpegPath(envString("FFMPEG_PATH", ""))
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	s := &Server{
		addr:         addr,
		dataDir:      dataDir,
		uploadsDir:   filepath.Join(dataDir, "uploads"),
		hlsDir:       filepath.Join(dataDir, "hls"),
		ffmpegPath:   ffmpegPath,
		store:        store.New(),
		transcodeSem: make(chan struct{}, 1),
		maxJobs:      envInt("MAX_JOBS", 3),
		jobTTL:       time.Duration(envInt("JOB_TTL_MINUTES", 120)) * time.Minute,
	}

	if err := os.MkdirAll(s.uploadsDir, 0o755); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(s.hlsDir, 0o755); err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("./web")))
	mux.Handle("/hls/", http.StripPrefix("/hls/", http.FileServer(http.Dir(s.hlsDir))))

	mux.HandleFunc("POST /api/upload", s.handleUpload)
	mux.HandleFunc("POST /api/jobs/", s.handleJobs) // /api/jobs/{id}/start
	mux.HandleFunc("GET /api/jobs/", s.handleJobs)  // /api/jobs/{id}
	mux.HandleFunc("GET /api/health", s.handleHealth)

	go s.cleanupLoop()

	fmt.Printf("AVI helper server listening on http://%s\n", s.addr)
	if err := http.ListenAndServe(s.addr, withCORS(mux)); err != nil {
		panic(err)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"ffmpeg":    s.ffmpegPath,
		"platform":  runtime.GOOS,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	jobID, err := newJobID()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed_to_generate_job_id"})
		return
	}

	jobUploadsDir := filepath.Join(s.uploadsDir, jobID)
	jobHLSDir := filepath.Join(s.hlsDir, jobID)
	if err := os.MkdirAll(jobUploadsDir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed_to_create_upload_dir"})
		return
	}
	if err := os.MkdirAll(jobHLSDir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed_to_create_hls_dir"})
		return
	}

	uploadPath := filepath.Join(jobUploadsDir, "input.avi")

	j := &store.Job{
		ID:         jobID,
		CreatedAt:  time.Now(),
		Status:     store.StatusUploading,
		UploadPath: uploadPath,
		HLSDir:     jobHLSDir,
	}
	s.store.Put(j)

	mr, err := r.MultipartReader()
	if err != nil {
		s.failJob(j.ID, "invalid_multipart_request")
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_multipart_request"})
		return
	}

	var filePartFound bool
	var srcFilename string

	dst, err := os.Create(uploadPath)
	if err != nil {
		s.failJob(j.ID, "failed_to_create_upload_file")
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed_to_create_upload_file"})
		return
	}
	defer dst.Close()

	for {
		part, perr := mr.NextPart()
		if errors.Is(perr, io.EOF) {
			break
		}
		if perr != nil {
			s.failJob(j.ID, "failed_to_read_multipart")
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "failed_to_read_multipart"})
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		filePartFound = true
		srcFilename = part.FileName()

		if _, err := io.Copy(dst, part); err != nil {
			_ = part.Close()
			s.failJob(j.ID, "failed_to_save_upload")
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed_to_save_upload"})
			return
		}
		_ = part.Close()
		break
	}

	if !filePartFound {
		s.failJob(j.ID, "missing_file_field")
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing_file_field"})
		return
	}
	if srcFilename != "" && !strings.HasSuffix(strings.ToLower(srcFilename), ".avi") {
		// Best-effort validation only; still proceed if it's AVI in content.
	}

	s.store.SetStatus(j.ID, store.StatusUploaded, "")
	writeJSON(w, http.StatusOK, map[string]any{"jobId": jobID})
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	// Expected:
	//   POST /api/jobs/{id}/start
	//   GET  /api/jobs/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
		return
	}
	jobID := parts[0]
	j, ok := s.store.Get(jobID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "job_not_found"})
		return
	}

	if r.Method == http.MethodGet {
		resp := *j
		if resp.PlaylistPath != "" {
			resp.PlaylistURL = "/hls/" + jobID + "/index.m3u8"
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "start" {
		if j.Status == store.StatusTranscoding || j.Status == store.StatusReady || j.Status == store.StatusFinished {
			resp := map[string]any{
				"jobId":       j.ID,
				"playlistUrl": "/hls/" + jobID + "/index.m3u8",
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		if j.Status != store.StatusUploaded {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "job_not_uploaded", "status": j.Status})
			return
		}

		if err := s.startTranscode(jobID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"jobId":       j.ID,
			"playlistUrl": "/hls/" + jobID + "/index.m3u8",
		})
		return
	}

	writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found"})
}

func (s *Server) startTranscode(jobID string) error {
	j, ok := s.store.Get(jobID)
	if !ok {
		return errors.New("job_not_found")
	}

	playlistPath := filepath.Join(j.HLSDir, "index.m3u8")
	logPath := filepath.Join(j.HLSDir, "ffmpeg.log")

	s.store.Update(jobID, func(x *store.Job) {
		x.Status = store.StatusTranscoding
		x.Error = ""
		x.PlaylistPath = playlistPath
		x.FFmpegLogPath = logPath
	})

	go func() {
		// Concurrency gate
		s.transcodeSem <- struct{}{}
		defer func() { <-s.transcodeSem }()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		pid, readyCh, doneCh, err := transcode.StartHLSAsync(ctx, transcode.Options{
			FFmpegPath:    s.ffmpegPath,
			InputPath:     j.UploadPath,
			OutDir:        j.HLSDir,
			PlaylistPath:  playlistPath,
			LogPath:       logPath,
			ReadyTimeout:  2 * time.Minute,
		})
		if err != nil {
			s.failJob(jobID, err.Error())
			return
		}
		s.store.Update(jobID, func(x *store.Job) { x.FFmpegPID = pid })

		<-readyCh
		// Best-effort: only mark ready if still transcoding.
		s.store.Update(jobID, func(x *store.Job) {
			if x.Status == store.StatusTranscoding {
				x.Status = store.StatusReady
				x.Error = ""
			}
		})

		if err := <-doneCh; err != nil {
			s.failJob(jobID, err.Error())
			return
		}
		s.store.SetStatus(jobID, store.StatusFinished, "")
	}()

	return nil
}

func (s *Server) cleanupLoop() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.cleanup()
	}
}

func (s *Server) cleanup() {
	s.store.Cleanup(store.CleanupPolicy{
		MaxJobs: s.maxJobs,
		TTL:     s.jobTTL,
	}, time.Now(), func(jobID string) {
		_ = os.RemoveAll(filepath.Join(s.uploadsDir, jobID))
		_ = os.RemoveAll(filepath.Join(s.hlsDir, jobID))
	})
}

func (s *Server) failJob(id string, msg string) {
	s.store.SetStatus(id, store.StatusFailed, msg)
}

func envString(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	var n int
	_, err := fmt.Sscanf(v, "%d", &n)
	if err != nil {
		return def
	}
	return n
}

func newJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// For local dev convenience (file:// or different port). Safe-ish since it's local.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func resolveFFmpegPath(env string) string {
	// Highest priority: explicit env var.
	if strings.TrimSpace(env) != "" {
		return env
	}

	// Then try common bundled locations relative to the current working directory.
	// When packaging, put ffmpeg at:
	//   ./bin/ffmpeg.exe (Windows) or ./bin/ffmpeg (Linux/macOS)
	//   ./third_party/ffmpeg/ffmpeg.exe (optional)
	candidates := []string{
		filepath.Join(".", "bin", ffmpegExeName()),
		filepath.Join(".", "third_party", "ffmpeg", ffmpegExeName()),
	}
	for _, p := range candidates {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func ffmpegExeName() string {
	if runtime.GOOS == "windows" {
		return "ffmpeg.exe"
	}
	return "ffmpeg"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !st.IsDir()
}

