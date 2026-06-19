package vrcclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseRequestDetectsVRChatAVPro(t *testing.T) {
	req, err := ParseRequest([]string{
		"-J",
		"--no-playlist",
		"https://example.com/watch?v=123",
	})
	if err != nil {
		t.Fatalf("ParseRequest returned error: %v", err)
	}
	if req.URL != "https://example.com/watch?v=123" {
		t.Fatalf("URL = %q", req.URL)
	}
	if !req.AVPro {
		t.Fatal("AVPro = false, want true")
	}
	if req.Source != "vrchat" {
		t.Fatalf("Source = %q, want vrchat", req.Source)
	}
}

func TestParseRequestDetectsUnityAndResonite(t *testing.T) {
	req, err := ParseRequest([]string{
		"-f",
		"best[protocol^=http]",
		"--flat-playlist",
		"https://example.com/video",
	})
	if err != nil {
		t.Fatalf("ParseRequest returned error: %v", err)
	}
	if req.AVPro {
		t.Fatal("AVPro = true, want false")
	}
	if req.Source != "resonite" {
		t.Fatalf("Source = %q, want resonite", req.Source)
	}
}

func TestParseRequestExtractsWrapperQueryOptions(t *testing.T) {
	req, err := ParseRequest([]string{
		"-J",
		"https://example.com/watch?v=123&vrcvp_transcode=true&quality=hd&vrcvp_profile=avpro&vrcvp_profile=fallback",
	})
	if err != nil {
		t.Fatalf("ParseRequest returned error: %v", err)
	}

	if got, want := req.URL, "https://example.com/watch?quality=hd&v=123"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	if got := req.Options["vrcvp_transcode"]; len(got) != 1 || got[0] != "true" {
		t.Fatalf("vrcvp_transcode = %#v, want [true]", got)
	}
	if got := req.Options["vrcvp_profile"]; len(got) != 2 || got[0] != "avpro" || got[1] != "fallback" {
		t.Fatalf("vrcvp_profile = %#v, want [avpro fallback]", got)
	}
}

func TestOptionsFromEnvironment(t *testing.T) {
	got := OptionsFromEnvironment([]string{
		"VRCVP_OPTION_TRANSCODE=true",
		"VRCVP_OPTION_PROFILE=avpro",
		"VRCVP_OPTION_EMPTY=",
		"VRCVP_SERVER_URL=http://127.0.0.1:8080",
		"NOT_VRCVP_OPTION=value",
	})

	if got.Get("vrcvp_transcode") != "true" {
		t.Fatalf("vrcvp_transcode = %q, want true", got.Get("vrcvp_transcode"))
	}
	if got.Get("vrcvp_profile") != "avpro" {
		t.Fatalf("vrcvp_profile = %q, want avpro", got.Get("vrcvp_profile"))
	}
	if _, ok := got["vrcvp_empty"]; ok {
		t.Fatalf("vrcvp_empty present in %#v, want empty env values ignored", got)
	}
}

func TestMergeOptionsLetsOverridesWin(t *testing.T) {
	got := MergeOptions(
		url.Values{
			"vrcvp_transcode": {"false"},
			"vrcvp_profile":   {"env"},
		},
		url.Values{
			"vrcvp_transcode": {"true"},
		},
	)

	if values := got["vrcvp_transcode"]; len(values) != 1 || values[0] != "true" {
		t.Fatalf("vrcvp_transcode = %#v, want [true]", values)
	}
	if values := got["vrcvp_profile"]; len(values) != 1 || values[0] != "env" {
		t.Fatalf("vrcvp_profile = %#v, want [env]", values)
	}
}

func TestParseRequestRequiresURL(t *testing.T) {
	if _, err := ParseRequest([]string{"-J", "--no-playlist"}); err == nil {
		t.Fatal("ParseRequest returned nil error, want error")
	}
}

func TestClientEndpoint(t *testing.T) {
	c := New("http://127.0.0.1:8080/base/", nil)
	endpoint, err := c.endpoint(Request{
		URL:    "https://example.com/watch?v=1&x=2",
		AVPro:  false,
		Source: "vrchat",
	})
	if err != nil {
		t.Fatalf("endpoint returned error: %v", err)
	}

	if !strings.HasPrefix(endpoint, "http://127.0.0.1:8080/base/api/getvideo?") {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if !strings.Contains(endpoint, "avpro=false") {
		t.Fatalf("endpoint = %q, missing avpro=false", endpoint)
	}
	if !strings.Contains(endpoint, "source=vrchat") {
		t.Fatalf("endpoint = %q, missing source=vrchat", endpoint)
	}
	if !strings.Contains(endpoint, "url=https%3A%2F%2Fexample.com%2Fwatch%3Fv%3D1%26x%3D2") {
		t.Fatalf("endpoint = %q, missing encoded url", endpoint)
	}
}

func TestClientEndpointIncludesWrapperOptions(t *testing.T) {
	c := New("http://127.0.0.1:8080", nil)
	endpoint, err := c.endpoint(Request{
		URL:    "https://example.com/watch?v=1",
		AVPro:  true,
		Source: "vrchat",
		Options: url.Values{
			"vrcvp_transcode": {"true"},
			"vrcvp_profile":   {"avpro", "fallback"},
		},
	})
	if err != nil {
		t.Fatalf("endpoint returned error: %v", err)
	}

	values, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("url.Parse returned error: %v", err)
	}
	query := values.Query()
	if got := query.Get("url"); got != "https://example.com/watch?v=1" {
		t.Fatalf("url query = %q", got)
	}
	if got := query["vrcvp_transcode"]; len(got) != 1 || got[0] != "true" {
		t.Fatalf("vrcvp_transcode query = %#v, want [true]", got)
	}
	if got := query["vrcvp_profile"]; len(got) != 2 || got[0] != "avpro" || got[1] != "fallback" {
		t.Fatalf("vrcvp_profile query = %#v, want [avpro fallback]", got)
	}
}

func TestClientEndpointRejectsBadServerURL(t *testing.T) {
	for _, server := range []string{"", "://nohost", "ftp://host:21"} {
		c := New(server, nil)
		if _, err := c.endpoint(Request{URL: "https://example.com/v"}); err == nil {
			t.Fatalf("endpoint(%q) = nil error, want error", server)
		}
	}
}

func TestClientResolveReturnsServerResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/getvideo" {
			t.Fatalf("path = %q, want /api/getvideo", r.URL.Path)
		}
		if got := r.URL.Query().Get("url"); got != "https://example.com/video" {
			t.Fatalf("url query = %q", got)
		}
		if got := r.URL.Query().Get("avpro"); got != "true" {
			t.Fatalf("avpro query = %q", got)
		}
		if got := r.URL.Query().Get("source"); got != "vrchat" {
			t.Fatalf("source query = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "http://127.0.0.1/stream/x.mp4"})
	}))
	defer ts.Close()

	body, err := New(ts.URL, ts.Client()).Resolve(context.Background(), Request{
		URL:    "https://example.com/video",
		AVPro:  true,
		Source: "vrchat",
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if !bytes.Contains(body, []byte(`"url"`)) {
		t.Fatalf("body = %q, want JSON response", body)
	}
}

func TestClientResolveReportsServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad url", http.StatusBadRequest)
	}))
	defer ts.Close()

	_, err := New(ts.URL, ts.Client()).Resolve(context.Background(), Request{
		URL:    "https://example.com/video",
		AVPro:  true,
		Source: "vrchat",
	})
	if err == nil {
		t.Fatal("Resolve returned nil error, want error")
	}
	if !strings.Contains(err.Error(), "bad url") {
		t.Fatalf("error = %q, want body included", err.Error())
	}
}
