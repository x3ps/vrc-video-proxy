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

func TestTranscodeArgsPerBackend(t *testing.T) {
	headers := map[string]string{}
	cases := []struct {
		name       string
		opts       transcodeOptions
		wantPair   map[string]string // -flag value pairs that must appear
		wantAbsent []string          // flags that must not appear
	}{
		{
			name: "software libx264 with preset and crf",
			opts: transcodeOptions{backend: hwSoftware, preset: "veryfast", crf: "23"},
			wantPair: map[string]string{
				"-c:v":     "libx264",
				"-preset":  "veryfast",
				"-crf":     "23",
				"-pix_fmt": "yuv420p",
			},
			wantAbsent: []string{"-vaapi_device", "-vf"},
		},
		{
			name: "nvenc uses target bitrate default and yuv420p",
			opts: transcodeOptions{backend: hwNVENC},
			wantPair: map[string]string{
				"-c:v":     "h264_nvenc",
				"-b:v":     "8M",
				"-pix_fmt": "yuv420p",
			},
			wantAbsent: []string{"-preset", "-crf", "-vaapi_device", "-vf"},
		},
		{
			name: "qsv uses nv12",
			opts: transcodeOptions{backend: hwQSV, videoBitrate: "6M"},
			wantPair: map[string]string{
				"-c:v":     "h264_qsv",
				"-b:v":     "6M",
				"-pix_fmt": "nv12",
			},
			wantAbsent: []string{"-vaapi_device", "-vf"},
		},
		{
			name: "vaapi sets device and upload filter, no pix_fmt",
			opts: transcodeOptions{backend: hwVAAPI, hwDevice: "/dev/dri/renderD128"},
			wantPair: map[string]string{
				"-vaapi_device": "/dev/dri/renderD128",
				"-c:v":          "h264_vaapi",
				"-vf":           "format=nv12,hwupload",
			},
			wantAbsent: []string{"-pix_fmt"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFfmpegRunner("ffmpeg", discardLogger(), c.opts)
			args := f.transcodeArgs("https://h/v", headers)
			for flag, want := range c.wantPair {
				i := slices.Index(args, flag)
				if i < 0 || i+1 >= len(args) || args[i+1] != want {
					t.Fatalf("expected %s %s in %v", flag, want, args)
				}
			}
			for _, flag := range c.wantAbsent {
				if slices.Contains(args, flag) {
					t.Fatalf("did not expect %s in %v", flag, args)
				}
			}
			// Audio is always re-encoded to AAC; output is fragmented MP4 on stdout.
			if i := slices.Index(args, "-c:a"); i < 0 || args[i+1] != "aac" {
				t.Fatalf("expected -c:a aac in %v", args)
			}
			if args[len(args)-1] != "pipe:1" {
				t.Fatalf("expected pipe:1 output in %v", args)
			}
		})
	}
}

func TestTranscodeArgsVAAPIDeviceBeforeInput(t *testing.T) {
	f := newFfmpegRunner("ffmpeg", discardLogger(), transcodeOptions{backend: hwVAAPI, hwDevice: "/dev/dri/renderD128"})
	args := f.transcodeArgs("https://h/v", map[string]string{})
	dev := slices.Index(args, "-vaapi_device")
	in := slices.Index(args, "-i")
	if dev < 0 || in < 0 || dev > in {
		t.Fatalf("-vaapi_device must precede -i in %v", args)
	}
}

func TestNewFfmpegRunnerDefaultsToSoftware(t *testing.T) {
	f := newFfmpegRunner("ffmpeg", discardLogger(), transcodeOptions{})
	if got := f.backend().encoder; got != "libx264" {
		t.Fatalf("zero-value backend = %q, want libx264", got)
	}
}

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{max: 8}
	tb.Write([]byte("0123456789ABCDEF"))
	if got := tb.String(); got != "89ABCDEF" {
		t.Fatalf("tail = %q, want 89ABCDEF", got)
	}
}
