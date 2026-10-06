package recording

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Entry is one line of the merged timeline.
type Entry struct {
	T    int64  // session nanoseconds
	Seq  uint64 // the record's sequence number, for ties
	Kind string // "http.request", "http.response", "http.error" or "browser.<type>"
	Text string
	// part orders the lines of one record: a request before its response.
	part int
}

// Timeline merges the HTTP and browser streams into one list ordered by
// session time. Ties fall back to the record sequence numbers, so the same
// recording always yields the same order whatever order its records are
// read in. Browser events that repeat the HTTP stream (CDP network events)
// or are pure bookkeeping are left out unless all is set.
func Timeline(r *Recording, all bool) []Entry {
	var out []Entry
	for i := range r.Exchanges {
		ex := &r.Exchanges[i]
		start := ex.Timing.RequestStart
		if start == 0 {
			start = ex.StartedAt.T
		}
		out = append(out, Entry{
			T: start, Seq: ex.Seq, Kind: "http.request",
			Text: fmt.Sprintf("%s %s", ex.Request.Method, ex.Request.URL),
		})
		switch {
		case ex.Response != nil:
			at := ex.Timing.ResponseStart
			if at == 0 {
				at = ex.CompletedAt.T
			}
			out = append(out, Entry{
				T: at, Seq: ex.Seq, Kind: "http.response", part: 1,
				Text: fmt.Sprintf("%d exchange=%s", ex.Response.Status, ex.ID),
			})
		case ex.Error != "":
			out = append(out, Entry{
				T: ex.CompletedAt.T, Seq: ex.Seq, Kind: "http.error", part: 1,
				Text: fmt.Sprintf("exchange=%s %s", ex.ID, ex.Error),
			})
		}
	}
	for i := range r.Events {
		ev := &r.Events[i]
		kind, text, shown := describeEvent(ev)
		if !shown && !all {
			continue
		}
		out = append(out, Entry{T: ev.Timestamp.T, Seq: ev.Seq, Kind: kind, Text: text})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.T != b.T {
			return a.T < b.T
		}
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		return a.part < b.part
	})
	return out
}

// WriteTimeline prints entries as "seconds kind text" lines.
func WriteTimeline(w io.Writer, entries []Entry) error {
	for _, e := range entries {
		if _, err := fmt.Fprintf(w, "%9.3f %-24s %s\n", float64(e.T)/1e9, e.Kind, e.Text); err != nil {
			return err
		}
	}
	return nil
}

// describeEvent names a browser event for the timeline and says whether
// it is shown by default.
func describeEvent(ev *BrowserEvent) (kind, text string, shown bool) {
	switch ev.Type {
	case "Page.frameNavigated":
		var p struct {
			Frame struct {
				ParentID string `json:"parentId"`
			} `json:"frame"`
		}
		json.Unmarshal(ev.Payload, &p)
		if p.Frame.ParentID != "" {
			return "browser.frame-navigation", ev.PageURL, false
		}
		return "browser.navigation", ev.PageURL, true
	case "Page.navigatedWithinDocument":
		return "browser.navigation", ev.PageURL + " (same document)", true
	case "Page.frameRequestedNavigation":
		var p struct {
			Reason string `json:"reason"`
			URL    string `json:"url"`
		}
		json.Unmarshal(ev.Payload, &p)
		// A frame's first load and about: documents are page plumbing, not
		// something the user asked for.
		shown := p.Reason != "initialFrameNavigation" && !strings.HasPrefix(p.URL, "about:")
		return "browser.navigation-request", strings.TrimSpace(p.Reason + " " + p.URL), shown
	case "Page.domContentEventFired":
		return "browser.domcontentloaded", ev.PageURL, false
	case "Page.loadEventFired":
		return "browser.load", ev.PageURL, true
	case "Target.targetCreated", "Target.targetDestroyed", "Target.targetInfoChanged":
		var p struct {
			TargetInfo struct {
				Type  string `json:"type"`
				Title string `json:"title"`
			} `json:"targetInfo"`
		}
		json.Unmarshal(ev.Payload, &p)
		name := map[string]string{
			"Target.targetCreated":     "browser.target-created",
			"Target.targetDestroyed":   "browser.target-destroyed",
			"Target.targetInfoChanged": "browser.target-changed",
		}[ev.Type]
		shown := ev.Type != "Target.targetInfoChanged" && p.TargetInfo.Type == "page"
		return name, strings.TrimSpace(p.TargetInfo.Type + " " + ev.PageURL), shown
	}
	if action, ok := strings.CutPrefix(ev.Type, "interaction."); ok {
		return "browser." + action, describeInteraction(ev.Payload), true
	}
	return "browser." + ev.Type, ev.PageURL, false
}

// describeInteraction summarizes an instrumented interaction, such as
// `role=searchbox name=field-keywords value="laptop"`.
func describeInteraction(payload json.RawMessage) string {
	var p struct {
		Target *struct {
			Role        string `json:"role"`
			Tag         string `json:"tag"`
			Name        string `json:"name"`
			Label       string `json:"label"`
			Placeholder string `json:"placeholder"`
			Text        string `json:"text"`
			Href        string `json:"href"`
		} `json:"target"`
		Value    *string `json:"value"`
		Redacted bool    `json:"redacted"`
		Checked  *bool   `json:"checked"`
		Action   string  `json:"action"`
		Method   string  `json:"method"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	if t := p.Target; t != nil {
		role := t.Role
		if role == "" {
			role = t.Tag
		}
		add("role", role)
		add("name", t.Name)
		label := t.Label
		if label == "" {
			label = t.Placeholder
		}
		if label != "" {
			add("label", strconv.Quote(label))
		}
		if t.Text != "" {
			add("text", strconv.Quote(t.Text))
		}
		add("href", t.Href)
	}
	switch {
	case p.Redacted:
		parts = append(parts, "value=<redacted>")
	case p.Value != nil:
		add("value", strconv.Quote(*p.Value))
	case p.Checked != nil:
		add("checked", strconv.FormatBool(*p.Checked))
	}
	if p.Action != "" {
		add("form", p.Method+" "+p.Action)
	}
	return strings.Join(parts, " ")
}
