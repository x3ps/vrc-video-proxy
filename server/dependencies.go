package main

import "fmt"

// checkRequiredExecutables verifies that the executables the server cannot work
// without are present.
func checkRequiredExecutables(lookup func(string) (string, error), ytdlpPath, ffmpegPath, ffprobePath string) error {
	required := []string{
		defaultExecutable(ytdlpPath, defaultYtdlpPath),
		defaultExecutable(ffmpegPath, defaultFfmpegPath),
		defaultExecutable(ffprobePath, defaultFfprobePath),
	}

	for _, path := range required {
		if _, err := lookup(path); err != nil {
			return fmt.Errorf("missing required executable: %s", path)
		}
	}
	return nil
}

func defaultExecutable(path, fallback string) string {
	if path == "" {
		return fallback
	}
	return path
}
