package transcode

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Options struct {
	FFmpegPath string

	InputPath    string
	OutDir       string
	PlaylistPath string
	LogPath      string

	ReadyTimeout time.Duration
}

// StartHLSAsync starts ffmpeg immediately and returns channels for readiness and completion.
// - ready closes when playlist becomes available (best-effort).
// - done receives nil on success, or a short string error (e.g. ffmpeg_failed).
func StartHLSAsync(ctx context.Context, opt Options) (pid int, ready <-chan struct{}, done <-chan error, _ error) {
	if opt.FFmpegPath == "" {
		opt.FFmpegPath = "ffmpeg"
	}
	if opt.ReadyTimeout <= 0 {
		opt.ReadyTimeout = 2 * time.Minute
	}

	if _, err := exec.LookPath(opt.FFmpegPath); err != nil {
		return 0, nil, nil, errors.New("ffmpeg_not_found")
	}

	if err := os.MkdirAll(opt.OutDir, 0o755); err != nil {
		return 0, nil, nil, err
	}

	args := hlsArgs(opt.InputPath, opt.PlaylistPath, opt.OutDir)
	cmd := exec.CommandContext(ctx, opt.FFmpegPath, args...)

	logFile, err := os.Create(opt.LogPath)
	if err != nil {
		return 0, nil, nil, err
	}
	cmd.Stdout = nil
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, nil, nil, err
	}

	if cmd.Process == nil {
		_ = logFile.Close()
		return 0, nil, nil, errors.New("failed_to_start_ffmpeg")
	}
	pid = cmd.Process.Pid

	readyCh := make(chan struct{})
	doneCh := make(chan error, 1)

	// readiness watcher
	go func() {
		deadline := time.Now().Add(opt.ReadyTimeout)
		for time.Now().Before(deadline) {
			fi, err := os.Stat(opt.PlaylistPath)
			if err == nil && fi.Size() > 0 {
				close(readyCh)
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		// If timeout, still close to unblock callers; they can decide what to do.
		close(readyCh)
	}()

	// completion watcher
	go func() {
		waitErr := cmd.Wait()
		_ = logFile.Close()
		if waitErr != nil {
			doneCh <- errors.New("ffmpeg_failed")
			return
		}
		doneCh <- nil
	}()

	return pid, readyCh, doneCh, nil
}

func hlsArgs(inputPath, playlistPath, outDir string) []string {
	segmentPattern := filepath.Join(outDir, "seg_%05d.ts")

	return []string{
		"-hide_banner",
		"-y",
		"-i", inputPath,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "23",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac",
		"-b:a", "128k",
		"-ac", "2",
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "8",
		"-hls_flags", "delete_segments+independent_segments",
		"-hls_segment_filename", segmentPattern,
		playlistPath,
	}
}

