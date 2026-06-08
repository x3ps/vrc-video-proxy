package main

import (
	"slices"
	"strings"
	"testing"
)

func TestFormatFFmpegHeaders(t *testing.T) {
	got := formatFFmpegHeaders(map[string]string{
		"Referer":    "https://example.com",
		"Cookie":     "sid=1",
		"User-Agent": "vrc/1.0", // excluded; passed via -user_agent
	})
	want := "Cookie: sid=1\r\nReferer: https://example.com\r\n"
	if got != want {
		t.Fatalf("formatFFmpegHeaders = %q, want %q", got, want)
	}
	if formatFFmpegHeaders(map[string]string{"User-Agent": "x"}) != "" {
		t.Fatal("UA-only headers should produce empty -headers string")
	}
}

func TestInputArgs(t *testing.T) {
	args := inputArgs("https://h/v.m3u8", map[string]string{
		"User-Agent": "vrc/1.0",
		"Referer":    "https://example.com",
	})
	if i := slices.Index(args, "-user_agent"); i < 0 || args[i+1] != "vrc/1.0" {
		t.Fatalf("expected -user_agent vrc/1.0 in %v", args)
	}
	if i := slices.Index(args, "-headers"); i < 0 || !strings.Contains(args[i+1], "Referer: https://example.com") {
		t.Fatalf("expected -headers with Referer in %v", args)
	}
	if args[len(args)-2] != "-i" || args[len(args)-1] != "https://h/v.m3u8" {
		t.Fatalf("input URL must be last: %v", args)
	}
}

func TestPickH264Encoder(t *testing.T) {
	// Realistic-ish `ffmpeg -encoders` fragment with NVENC and software present.
	out := " V..... libx264              libx264 H.264\n V..... h264_nvenc           NVIDIA NVENC H.264\n V..... h264_qsv             QuickSync H.264\n"
	if got := pickH264Encoder(out); got != "h264_nvenc" {
		t.Fatalf("pickH264Encoder = %q, want h264_nvenc (highest priority present)", got)
	}
	if got := pickH264Encoder(" V..... libx264   libx264 H.264\n"); got != "libx264" {
		t.Fatalf("pickH264Encoder fallback = %q, want libx264", got)
	}
}

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{max: 8}
	tb.Write([]byte("0123456789ABCDEF"))
	if got := tb.String(); got != "89ABCDEF" {
		t.Fatalf("tail = %q, want 89ABCDEF", got)
	}
}
