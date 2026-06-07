package main

import (
	"errors"
	"testing"
)

func TestCheckRequiredExecutablesPassesWhenYtdlpExists(t *testing.T) {
	err := checkRequiredExecutables(func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}, "yt-dlp")

	if err != nil {
		t.Fatalf("checkRequiredExecutables returned error: %v", err)
	}
}

func TestCheckRequiredExecutablesReportsMissingYtdlp(t *testing.T) {
	err := checkRequiredExecutables(func(name string) (string, error) {
		return "", errors.New("not found")
	}, "yt-dlp")

	if err == nil {
		t.Fatal("checkRequiredExecutables returned nil, want error")
	}

	if got, want := err.Error(), "missing required executable: yt-dlp"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCheckRequiredExecutablesUsesConfiguredPath(t *testing.T) {
	var looked string
	err := checkRequiredExecutables(func(name string) (string, error) {
		looked = name
		return "/opt/yt-dlp", nil
	}, "/opt/yt-dlp")

	if err != nil {
		t.Fatalf("checkRequiredExecutables returned error: %v", err)
	}
	if looked != "/opt/yt-dlp" {
		t.Fatalf("looked up %q, want /opt/yt-dlp", looked)
	}
}

func TestCheckRequiredExecutablesDoesNotRequireFfmpeg(t *testing.T) {
	// ffmpeg is not in the required set, so a lookup that only fails for ffmpeg
	// must still succeed.
	err := checkRequiredExecutables(func(name string) (string, error) {
		if name == "ffmpeg" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}, "yt-dlp")

	if err != nil {
		t.Fatalf("checkRequiredExecutables returned error: %v", err)
	}
}
