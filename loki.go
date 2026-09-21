package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LogLine is one collector line of a task, normalized: the wrapper's own
// "[ts] guid=... msg" lines become stream "wrap", the JSON records it
// frames stdout/stderr into keep their stream. Cursor is the collector's
// nanosecond timestamp and is what paging uses; TS is the wrapper's own
// clock, which is what the reader should see.
type LogLine struct {
	Cursor  string `json:"cursor"`
	TS      string `json:"ts"`
	Stream  string `json:"stream"`
	Line    string `json:"line"`
	Partial bool   `json:"partial,omitempty"`
	Root    string `json:"-"`
}

type LogResp struct {
	GUID  string    `json:"guid"`
	Root  string    `json:"root,omitempty"`
	Lines []LogLine `json:"lines"`
}

var wrapLogLineRe = regexp.MustCompile(`^\[(\S+)\] guid=(\S+) (.*)$`)

// parseTaskLogLine keeps only the lines that belong to guid: the |= filter
// the query uses is a substring match, so a guid that is a prefix of
// another one would otherwise leak the other task's output in.
func parseTaskLogLine(guid, cursor, raw string) (LogLine, bool) {
	if strings.HasPrefix(raw, "{") {
		var rec wrapTaskLogRecord

		if json.Unmarshal([]byte(raw), &rec) != nil || rec.GUID != guid {
			return LogLine{}, false
		}

		return LogLine{Cursor: cursor, TS: rec.TS, Stream: rec.Stream, Line: rec.Line, Partial: rec.Partial, Root: rec.Root}, true
	}

	m := wrapLogLineRe.FindStringSubmatch(raw)

	if m == nil || m[2] != guid {
		return LogLine{}, false
	}

	return LogLine{Cursor: cursor, TS: m[1], Stream: "wrap", Line: m[3]}, true
}

func cursorLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}

	return a < b
}

type lokiClient struct {
	base     string
	selector string
	http     *http.Client
}

type lokiQueryResp struct {
	Data struct {
		Result []struct {
			Values [][2]string `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// taskLog runs one query_range against Loki and returns the task's lines
// in ascending cursor order regardless of the direction asked for.
// direction picks which end of the window a limit-truncated answer keeps.
func (c *lokiClient) taskLog(ctx context.Context, guid string, start, end time.Time, direction string, limit int) []LogLine {
	q := url.Values{}
	q.Set("query", c.selector+" |= "+strconv.Quote(guid))
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("direction", direction)

	req := Throw2(http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/loki/api/v1/query_range?"+q.Encode(), nil))
	resp := Throw2(c.http.Do(req))

	defer resp.Body.Close()

	body := Throw2(io.ReadAll(resp.Body))

	if resp.StatusCode != http.StatusOK {
		ThrowFmt("loki: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed lokiQueryResp
	Throw(json.Unmarshal(body, &parsed))

	lines := []LogLine{}

	for _, stream := range parsed.Data.Result {
		for _, value := range stream.Values {
			if line, ok := parseTaskLogLine(guid, value[0], value[1]); ok {
				lines = append(lines, line)
			}
		}
	}

	sort.SliceStable(lines, func(i, j int) bool {
		return cursorLess(lines[i].Cursor, lines[j].Cursor)
	})

	return lines
}
