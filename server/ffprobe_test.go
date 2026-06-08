package main

import (
	"encoding/json"
	"testing"
)

// sample ffprobe `-print_format json -show_format -show_streams` output.
const sampleFfprobeJSON = `{
  "streams": [
    {"index": 0, "codec_name": "h264", "codec_type": "video", "width": 1920, "height": 1080, "bit_rate": "4500000", "duration": "634.512000"},
    {"index": 1, "codec_name": "aac", "codec_type": "audio", "bit_rate": "128000", "duration": "634.560000"}
  ],
  "format": {
    "filename": "video.mp4",
    "format_name": "mov,mp4,m4a,3gp,3g2,mj2",
    "format_long_name": "QuickTime / MOV",
    "duration": "634.512000",
    "size": "367001600",
    "bit_rate": "4628000",
    "probe_score": 100
  }
}`

func TestFfprobeParseResult(t *testing.T) {
	var r ffprobeResult
	if err := json.Unmarshal([]byte(sampleFfprobeJSON), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Format.FormatName != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Fatalf("FormatName = %q", r.Format.FormatName)
	}
	if r.Format.ProbeScore != 100 {
		t.Fatalf("ProbeScore = %d, want 100", r.Format.ProbeScore)
	}
	if len(r.Streams) != 2 {
		t.Fatalf("got %d streams, want 2", len(r.Streams))
	}
	if v := r.Streams[0]; v.CodecName != "h264" || v.CodecType != "video" || v.Width != 1920 || v.Height != 1080 {
		t.Fatalf("video stream parsed wrong: %+v", v)
	}
	if a := r.Streams[1]; a.CodecName != "aac" || a.CodecType != "audio" {
		t.Fatalf("audio stream parsed wrong: %+v", a)
	}
}

func TestFfprobeVideoAudioStream(t *testing.T) {
	var r ffprobeResult
	if err := json.Unmarshal([]byte(sampleFfprobeJSON), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v := r.videoStream(); v == nil || v.CodecName != "h264" {
		t.Fatalf("videoStream = %+v, want h264", v)
	}
	if a := r.audioStream(); a == nil || a.CodecName != "aac" {
		t.Fatalf("audioStream = %+v, want aac", a)
	}

	audioOnly := ffprobeResult{Streams: []ffprobeStream{{CodecName: "mp3", CodecType: "audio"}}}
	if v := audioOnly.videoStream(); v != nil {
		t.Fatalf("videoStream = %+v, want nil when no video", v)
	}
	if a := audioOnly.audioStream(); a == nil {
		t.Fatal("audioStream = nil, want the mp3 stream")
	}
}

func TestFfprobeDurationSeconds(t *testing.T) {
	var r ffprobeResult
	if err := json.Unmarshal([]byte(sampleFfprobeJSON), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := r.durationSeconds(); got != 634.512 {
		t.Fatalf("durationSeconds = %v, want 634.512", got)
	}

	// Falls back to the video stream duration when the format omits it.
	streamOnly := ffprobeResult{Streams: []ffprobeStream{{CodecType: "video", Duration: "12.5"}}}
	if got := streamOnly.durationSeconds(); got != 12.5 {
		t.Fatalf("fallback durationSeconds = %v, want 12.5", got)
	}

	// Empty or garbage durations yield 0 rather than an error.
	empty := ffprobeResult{}
	if got := empty.durationSeconds(); got != 0 {
		t.Fatalf("empty durationSeconds = %v, want 0", got)
	}
	garbage := ffprobeResult{Format: ffprobeFormat{Duration: "N/A"}}
	if got := garbage.durationSeconds(); got != 0 {
		t.Fatalf("garbage durationSeconds = %v, want 0", got)
	}
}
