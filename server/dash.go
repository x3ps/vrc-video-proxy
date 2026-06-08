package main

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Eyevinn/dash-mpd/mpd"
)

// isMPD reports whether a manifest body is an MPEG-DASH MPD (vs an HLS m3u8), by
// sniffing the leading bytes.
func isMPD(body []byte) bool {
	s := bytes.TrimSpace(body)
	if bytes.HasPrefix(s, []byte("#EXTM3U")) {
		return false
	}
	head := s
	if len(head) > 256 {
		head = head[:256]
	}
	return bytes.HasPrefix(s, []byte("<?xml")) || bytes.Contains(head, []byte("<MPD"))
}

// dashTemplateRe matches DASH SegmentTemplate identifiers like $Number$,
// $Time$, $RepresentationID$, $Bandwidth$, an optional printf-style width
// ($Number%05d$), and the literal $$.
var dashTemplateRe = regexp.MustCompile(`\$\$|\$(RepresentationID|Bandwidth|Number|Time)(%0?\d+[diouxX])?\$`)

// expandDASHTemplate fills a SegmentTemplate string for one segment.
func expandDASHTemplate(tmpl, repID string, bandwidth uint32, number, time uint64) string {
	return dashTemplateRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		if m == "$$" {
			return "$"
		}
		sub := dashTemplateRe.FindStringSubmatch(m)
		id, format := sub[1], sub[2]
		switch id {
		case "RepresentationID":
			return repID
		case "Bandwidth":
			return formatDASHNumber(uint64(bandwidth), format)
		case "Number":
			return formatDASHNumber(number, format)
		case "Time":
			return formatDASHNumber(time, format)
		default:
			return m
		}
	})
}

// formatDASHNumber renders v, honoring an optional printf width like "%05d".
func formatDASHNumber(v uint64, format string) string {
	if format == "" {
		return strconv.FormatUint(v, 10)
	}
	return fmt.Sprintf(format, v)
}

// segTiming is one segment's number and media-timescale start time + duration.
type segTiming struct {
	number uint64
	time   uint64
	d      uint64
}

// timelineSegments expands a SegmentTimeline (the <S> t/d/r entries) into a flat
// list of segment timings, numbering from startNumber. An unbounded repeat
// (r < 0, only valid for a still-updating live edge) is treated as no repeat for
// this snapshot; the player refetches the manifest for newer segments.
func timelineSegments(tl *mpd.SegmentTimelineType, startNumber uint64) []segTiming {
	var out []segTiming
	num := startNumber
	var t uint64
	for _, s := range tl.S {
		if s.T != nil {
			t = *s.T
		}
		reps := s.R
		if reps < 0 {
			reps = 0
		}
		for i := 0; i <= reps; i++ {
			out = append(out, segTiming{number: num, time: t, d: s.D})
			t += s.D
			num++
		}
	}
	return out
}

// dashToHLS converts a live DASH MPD into an HLS media playlist for its primary
// video representation, so the existing /hls/segment proxy serves the segments
// and AVPro plays plain HLS. seg builds a proxied URL for an absolute segment
// URL. Only SegmentTemplate-with-SegmentTimeline manifests are supported; other
// shapes return an error so the caller can surface a clear status.
func dashToHLS(body []byte, manifestURL string, seg func(absURL string) string) (string, error) {
	m, err := mpd.ReadFromString(string(body))
	if err != nil {
		return "", fmt.Errorf("parse mpd: %w", err)
	}
	if len(m.Periods) == 0 {
		return "", errors.New("mpd has no periods")
	}
	period := m.Periods[0]

	rep, as := selectVideoRepresentation(period)
	if rep == nil {
		return "", errors.New("mpd has no usable representation")
	}
	st := firstSegmentTemplate(rep.SegmentTemplate, as.SegmentTemplate, period.SegmentTemplate)
	if st == nil || st.Media == "" {
		return "", errors.New("unsupported mpd: no SegmentTemplate media")
	}
	if st.SegmentTimeline == nil {
		return "", errors.New("unsupported mpd: only SegmentTimeline live is supported")
	}

	base, err := resolveDASHBase(manifestURL, m, period, as, rep)
	if err != nil {
		return "", err
	}
	timescale := uint64(st.GetTimescale())
	if timescale == 0 {
		timescale = 1
	}
	startNumber := uint64(1)
	if st.StartNumber != nil {
		startNumber = uint64(*st.StartNumber)
	}

	segs := timelineSegments(st.SegmentTimeline, startNumber)
	if len(segs) == 0 {
		return "", errors.New("mpd timeline produced no segments")
	}

	var body2 strings.Builder
	maxDur := 0.0
	for _, sg := range segs {
		if d := float64(sg.d) / float64(timescale); d > maxDur {
			maxDur = d
		}
	}

	body2.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	fmt.Fprintf(&body2, "#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(maxDur)))
	fmt.Fprintf(&body2, "#EXT-X-MEDIA-SEQUENCE:%d\n", startNumber)
	if st.Initialization != "" {
		initPath := expandDASHTemplate(st.Initialization, rep.Id, rep.Bandwidth, 0, 0)
		fmt.Fprintf(&body2, "#EXT-X-MAP:URI=%q\n", seg(resolveRef(base, initPath)))
	}
	for _, sg := range segs {
		mediaPath := expandDASHTemplate(st.Media, rep.Id, rep.Bandwidth, sg.number, sg.time)
		dur := float64(sg.d) / float64(timescale)
		fmt.Fprintf(&body2, "#EXTINF:%.3f,\n%s\n", dur, seg(resolveRef(base, mediaPath)))
	}
	// Dynamic (live) playlist: no EXT-X-ENDLIST, so the player keeps refetching.
	return body2.String(), nil
}

// selectVideoRepresentation picks the highest-bandwidth video representation in
// the period, falling back to the first representation of the first adaptation
// set when no explicit video type is present.
func selectVideoRepresentation(period *mpd.Period) (*mpd.RepresentationType, *mpd.AdaptationSetType) {
	var bestRep *mpd.RepresentationType
	var bestAS *mpd.AdaptationSetType
	var fallbackRep *mpd.RepresentationType
	var fallbackAS *mpd.AdaptationSetType

	for _, as := range period.AdaptationSets {
		isVideo := strings.EqualFold(string(as.ContentType), "video") ||
			strings.HasPrefix(strings.ToLower(as.GetMimeType()), "video/")
		for _, rep := range as.Representations {
			if fallbackRep == nil {
				fallbackRep, fallbackAS = rep, as
			}
			if isVideo && (bestRep == nil || rep.Bandwidth > bestRep.Bandwidth) {
				bestRep, bestAS = rep, as
			}
		}
	}
	if bestRep != nil {
		return bestRep, bestAS
	}
	return fallbackRep, fallbackAS
}

// firstSegmentTemplate returns the first non-nil template (Representation wins
// over AdaptationSet over Period, per DASH inheritance).
func firstSegmentTemplate(templates ...*mpd.SegmentTemplateType) *mpd.SegmentTemplateType {
	for _, t := range templates {
		if t != nil {
			return t
		}
	}
	return nil
}

// resolveDASHBase resolves the effective base URL for segments by applying the
// MPD/Period/AdaptationSet/Representation BaseURL chain against the manifest URL.
func resolveDASHBase(manifestURL string, m *mpd.MPD, period *mpd.Period, as *mpd.AdaptationSetType, rep *mpd.RepresentationType) (*url.URL, error) {
	base, err := url.Parse(manifestURL)
	if err != nil {
		return nil, fmt.Errorf("parse manifest url: %w", err)
	}
	for _, level := range [][]*mpd.BaseURLType{m.BaseURL, period.BaseURLs, as.BaseURLs, rep.BaseURLs} {
		if len(level) == 0 {
			continue
		}
		ref, err := url.Parse(strings.TrimSpace(string(level[0].Value)))
		if err != nil {
			continue
		}
		base = base.ResolveReference(ref)
	}
	return base, nil
}
