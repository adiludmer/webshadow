package recording

import (
	"fmt"
	"io"
	"sort"
)

// Entry is one line of the timeline.
type Entry struct {
	T    int64  // session nanoseconds
	Seq  uint64 // the record's sequence number, for ties
	Kind string // "http.request", "http.response", "http.error", or a caller's own
	Text string
	// Part orders the lines of one record: a caller's line, such as the
	// page view an exchange starts, before its request, and the request
	// before its response.
	Part int
}

// Timeline lists the exchanges' requests and responses, and any extra
// entries, ordered by session time. Ties fall back to the record sequence
// numbers, so the same recording always yields the same order whatever
// order its records are read in.
func Timeline(r *Recording, extra ...Entry) []Entry {
	out := append([]Entry(nil), extra...)
	for i := range r.Exchanges {
		ex := &r.Exchanges[i]
		start := ex.Timing.RequestStart
		if start == 0 {
			start = ex.StartedAt.T
		}
		out = append(out, Entry{
			T: start, Seq: ex.Seq, Kind: "http.request", Part: 1,
			Text: fmt.Sprintf("%s %s", ex.Request.Method, ex.Request.URL),
		})
		switch {
		case ex.Response != nil:
			at := ex.Timing.ResponseStart
			if at == 0 {
				at = ex.CompletedAt.T
			}
			out = append(out, Entry{
				T: at, Seq: ex.Seq, Kind: "http.response", Part: 2,
				Text: fmt.Sprintf("%d exchange=%s", ex.Response.Status, ex.ID),
			})
		case ex.Error != "":
			out = append(out, Entry{
				T: ex.CompletedAt.T, Seq: ex.Seq, Kind: "http.error", Part: 2,
				Text: fmt.Sprintf("exchange=%s %s", ex.ID, ex.Error),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.T != b.T {
			return a.T < b.T
		}
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		return a.Part < b.Part
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
