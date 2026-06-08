package main

import (
	"strings"
	"testing"

	"github.com/Eyevinn/dash-mpd/mpd"
)

func TestExpandDASHTemplate(t *testing.T) {
	tests := []struct {
		tmpl   string
		number uint64
		time   uint64
		want   string
	}{
		{"seg-$RepresentationID$-$Number$.m4s", 12, 0, "seg-v0-12.m4s"},
		{"seg-$Number%05d$.m4s", 12, 0, "seg-00012.m4s"},
		{"chunk-$Time$.m4s", 0, 9000, "chunk-9000.m4s"},
		{"$Bandwidth$/$Number$", 3, 0, "1000000/3"},
		{"a$$b", 0, 0, "a$b"},
	}
	for _, tt := range tests {
		got := expandDASHTemplate(tt.tmpl, "v0", 1000000, tt.number, tt.time)
		if got != tt.want {
			t.Errorf("expandDASHTemplate(%q) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
}

func TestTimelineSegments(t *testing.T) {
	z := uint64(0)
	tl := &mpd.SegmentTimelineType{S: []*mpd.S{
		{T: &z, D: 100, R: 2}, // 3 segments at t=0,100,200
		{D: 50, R: 0},         // 1 segment at t=300
	}}
	segs := timelineSegments(tl, 5)
	if len(segs) != 4 {
		t.Fatalf("got %d segments, want 4", len(segs))
	}
	wantTimes := []uint64{0, 100, 200, 300}
	for i, sg := range segs {
		if sg.number != uint64(5+i) {
			t.Errorf("segment %d number = %d, want %d", i, sg.number, 5+i)
		}
		if sg.time != wantTimes[i] {
			t.Errorf("segment %d time = %d, want %d", i, sg.time, wantTimes[i])
		}
	}
}

const liveDASHManifest = `<?xml version="1.0"?>
<MPD type="dynamic" xmlns="urn:mpeg:dash:schema:mpd:2011" profiles="urn:mpeg:dash:profile:isoff-live:2011">
  <Period id="0">
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <SegmentTemplate timescale="90000" initialization="init-$RepresentationID$.mp4" media="seg-$RepresentationID$-$Number$.m4s" startNumber="1">
        <SegmentTimeline>
          <S t="0" d="180000" r="2"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="1000000" width="1280" height="720" codecs="avc1.64001f"/>
    </AdaptationSet>
  </Period>
</MPD>`

func TestDASHToHLS(t *testing.T) {
	seg := func(abs string) string { return "S|" + abs }
	out, err := dashToHLS([]byte(liveDASHManifest), "https://h.com/live/manifest.mpd", seg)
	if err != nil {
		t.Fatalf("dashToHLS: %v", err)
	}

	checks := []string{
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:1",
		`#EXT-X-MAP:URI="S|https://h.com/live/init-v0.mp4"`,
		"#EXTINF:2.000,\nS|https://h.com/live/seg-v0-1.m4s",
		"S|https://h.com/live/seg-v0-2.m4s",
		"S|https://h.com/live/seg-v0-3.m4s",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("output missing %q:\n%s", c, out)
		}
	}
	if strings.Count(out, "#EXTINF:") != 3 {
		t.Errorf("expected 3 segments, got:\n%s", out)
	}
	if strings.Contains(out, "#EXT-X-ENDLIST") {
		t.Errorf("live playlist must not contain ENDLIST:\n%s", out)
	}
}

func TestDASHToHLSUnsupported(t *testing.T) {
	// SegmentTemplate without a SegmentTimeline is rejected with a clear error.
	const noTimeline = `<?xml version="1.0"?>
<MPD type="dynamic" xmlns="urn:mpeg:dash:schema:mpd:2011">
  <Period><AdaptationSet contentType="video" mimeType="video/mp4">
    <SegmentTemplate timescale="90000" media="seg-$Number$.m4s" duration="180000"/>
    <Representation id="v0" bandwidth="1000000"/>
  </AdaptationSet></Period>
</MPD>`
	if _, err := dashToHLS([]byte(noTimeline), "https://h/m.mpd", func(s string) string { return s }); err == nil {
		t.Fatal("expected error for unsupported MPD shape")
	}
}

func TestIsMPD(t *testing.T) {
	if !isMPD([]byte("<?xml version=\"1.0\"?>\n<MPD></MPD>")) {
		t.Error("xml MPD not detected")
	}
	if !isMPD([]byte("  <MPD type=\"dynamic\">")) {
		t.Error("bare MPD not detected")
	}
	if isMPD([]byte("#EXTM3U\n#EXTINF:4,\nseg.ts")) {
		t.Error("m3u8 misdetected as MPD")
	}
}
