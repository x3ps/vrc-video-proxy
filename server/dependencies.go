package main

import "fmt"

// checkRequiredExecutables verifies that the executables the server cannot work
// without are present. Only yt-dlp is required: extraction depends on it.
//
// ffmpeg is intentionally not required here because the current pipeline caches
// the raw progressive stream without transcoding. Transcoding (and its ffmpeg
// dependency check) is left for a future iteration.
func checkRequiredExecutables(lookup func(string) (string, error), ytdlpPath string) error {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}
	if _, err := lookup(ytdlpPath); err != nil {
		return fmt.Errorf("missing required executable: %s", ytdlpPath)
	}
	return nil
}
