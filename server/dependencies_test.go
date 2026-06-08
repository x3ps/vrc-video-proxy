package main

import (
	"errors"
	"testing"
)

func TestCheckRequiredExecutablesPassesWhenYtdlpExists(t *testing.T) {
	err := checkRequiredExecutables(func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}, "yt-dlp", "ffmpeg", "ffprobe")

	if err != nil {
		t.Fatalf("checkRequiredExecutables returned error: %v", err)
	}
}

func TestCheckRequiredExecutablesReportsMissingYtdlp(t *testing.T) {
	err := checkRequiredExecutables(func(name string) (string, error) {
		return "", errors.New("not found")
	}, "yt-dlp", "ffmpeg", "ffprobe")

	if err == nil {
		t.Fatal("checkRequiredExecutables returned nil, want error")
	}

	if got, want := err.Error(), "missing required executable: yt-dlp"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCheckRequiredExecutablesUsesConfiguredPath(t *testing.T) {
	var looked []string
	err := checkRequiredExecutables(func(name string) (string, error) {
		looked = append(looked, name)
		return name, nil
	}, "/opt/yt-dlp", "/opt/ffmpeg", "/opt/ffprobe")

	if err != nil {
		t.Fatalf("checkRequiredExecutables returned error: %v", err)
	}

	want := []string{"/opt/yt-dlp", "/opt/ffmpeg", "/opt/ffprobe"}
	if len(looked) != len(want) {
		t.Fatalf("looked up %v, want %v", looked, want)
	}
	for i := range want {
		if looked[i] != want[i] {
			t.Fatalf("looked up %v, want %v", looked, want)
		}
	}
}

func TestCheckRequiredExecutablesReportsMissingFfmpeg(t *testing.T) {
	err := checkRequiredExecutables(func(name string) (string, error) {
		if name == "ffmpeg" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}, "yt-dlp", "ffmpeg", "ffprobe")

	if err == nil {
		t.Fatal("checkRequiredExecutables returned nil, want error")
	}

	if got, want := err.Error(), "missing required executable: ffmpeg"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCheckRequiredExecutablesReportsMissingFfprobe(t *testing.T) {
	err := checkRequiredExecutables(func(name string) (string, error) {
		if name == "ffprobe" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}, "yt-dlp", "ffmpeg", "ffprobe")

	if err == nil {
		t.Fatal("checkRequiredExecutables returned nil, want error")
	}

	if got, want := err.Error(), "missing required executable: ffprobe"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}
