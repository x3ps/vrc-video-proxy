package main

import (
	"errors"
	"testing"
)

func TestCheckYtdlpPassesWhenPresent(t *testing.T) {
	if err := checkYtdlp(func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}, "yt-dlp"); err != nil {
		t.Fatalf("checkYtdlp returned error: %v", err)
	}
}

func TestCheckYtdlpReportsMissing(t *testing.T) {
	err := checkYtdlp(func(string) (string, error) {
		return "", errors.New("not found")
	}, "yt-dlp")
	if err == nil {
		t.Fatal("checkYtdlp returned nil, want error")
	}
	if got, want := err.Error(), "missing required executable: yt-dlp"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCheckYtdlpUsesConfiguredPath(t *testing.T) {
	var looked string
	if err := checkYtdlp(func(name string) (string, error) {
		looked = name
		return name, nil
	}, "/opt/yt-dlp"); err != nil {
		t.Fatalf("checkYtdlp returned error: %v", err)
	}
	if looked != "/opt/yt-dlp" {
		t.Fatalf("looked up %q, want /opt/yt-dlp", looked)
	}
}
