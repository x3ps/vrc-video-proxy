package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunResolvesAndWritesURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("http://127.0.0.1:8080/stream/abc.mp4"))
	}))
	defer ts.Close()

	t.Setenv(envServerURL, ts.URL)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-J", "--no-playlist", "https://example.com/watch?v=1"}, &stdout, &stderr, ts.Client())

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %s", code, stderr.String())
	}
	if got := stdout.String(); got != "http://127.0.0.1:8080/stream/abc.mp4\n" {
		t.Fatalf("stdout = %q, want the URL with a trailing newline", got)
	}
}

func TestRunReportsMissingURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-J", "--no-playlist"}, &stdout, &stderr, http.DefaultClient)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on failure", stdout.String())
	}
}
