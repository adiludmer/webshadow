// Package values builds an inverted index from every observed scalar value
// to every place it occurred, and derives value links: one exact value seen
// in more than one family. A link is how a join shows up before anything is
// named. A product id in a search response and the same id in a product
// request's path become one link; nothing here calls either a product.
//
// Weak values are kept. A boolean or a small number is flagged
// low_information by normalization, and a value seen across many families is
// flagged ubiquitous here; both stay in the index and in the links, and a
// later reader decides what to show. The one value left out is the empty
// string, which carries nothing to join on.
package values

import (
	"regexp"
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/family"
	"github.com/adiludmer/webshadow/internal/cluster/model"
)

// Options tune the index. The defaults are part of the deterministic
// contract: changing one changes which values are flagged or found.
type Options struct {
	// UbiquitousFamilies is the family count above which a value is flagged
	// ubiquitous.
	UbiquitousFamilies int
	// MinTextValueLen is the shortest request value searched for inside text
	// bodies. Short tokens recur in any page by chance.
	MinTextValueLen int
	// MaxTextBytes is the largest text body searched.
	MaxTextBytes int64
}

// DefaultOptions is what the clustering pass uses.
func DefaultOptions() Options {
	return Options{UbiquitousFamilies: 10, MinTextValueLen: 6, MaxTextBytes: 16 << 20}
}

// TextSource returns the decoded bytes of an observation's response body,
// for bodies that were not parsed structurally. It reports false when the
// bytes are not available.
type TextSource func(o *model.Observation) ([]byte, bool)

// Index maps each value to every occurrence of it.
type Index struct {
	opts   Options
	occ    map[string][]model.Occurrence
	types  map[string]map[string]bool
	weak   map[string]bool
	family map[string]map[string]bool
}

// Build indexes the values of the observations. text may be nil, in which
// case unparsed text bodies are not searched.
func Build(obs []model.Observation, fams family.Result, text TextSource, opts Options) *Index {
	if opts.UbiquitousFamilies == 0 {
		opts = DefaultOptions()
	}
	ix := &Index{
		opts:   opts,
		occ:    map[string][]model.Occurrence{},
		types:  map[string]map[string]bool{},
		weak:   map[string]bool{},
		family: map[string]map[string]bool{},
	}
	for i := range obs {
		o := &obs[i]
		fam := fams.ByObservation[o.Ref().Key()]
		for _, v := range o.Values {
			if v.Value == "" {
				continue
			}
			ix.add(v.Value, v.Type, v.HasFlag(model.FlagLowInformation), model.Occurrence{Ref: o.Ref(), FamilyID: fam, Loc: v.Loc})
		}
	}
	if text != nil {
		ix.scanText(obs, fams, text)
	}
	for v := range ix.occ {
		sortOccurrences(ix.occ[v])
	}
	return ix
}

func (ix *Index) add(value, typ string, weak bool, o model.Occurrence) {
	ix.occ[value] = append(ix.occ[value], o)
	if ix.types[value] == nil {
		ix.types[value] = map[string]bool{}
		ix.family[value] = map[string]bool{}
	}
	ix.types[value][typ] = true
	if weak {
		ix.weak[value] = true
	}
	if o.FamilyID != "" {
		ix.family[value][o.FamilyID] = true
	}
}

// textMedia are the media types whose unparsed bodies are searched for
// request values. Scripts are included because pages hand identifiers to
// their scripts as often as to their markup.
var textMedia = map[string]bool{
	"text/html": true, "application/xhtml+xml": true,
	"text/plain": true, "text/xml": true, "application/xml": true,
	"text/javascript": true, "application/javascript": true, "application/x-javascript": true,
	"application/json": true,
}

// scannable matches values that can be found as a whole token. A value with
// spaces, slashes or dots would need the page's own encoding rules to find,
// so it is left to the structured index.
var scannable = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// scanText finds request-side values inside unparsed text responses, such as
// product ids in a search results page, so the page can link to the
// requests that later carry them. Only values that some request carried are
// looked for; a page's own tokens are not indexed wholesale.
func (ix *Index) scanText(obs []model.Observation, fams family.Result, text TextSource) {
	candidates := map[string]bool{}
	for v, list := range ix.occ {
		if len(v) < ix.opts.MinTextValueLen || !scannable.MatchString(v) {
			continue
		}
		for _, o := range list {
			if o.Loc.Side == model.SideRequest {
				candidates[v] = true
				break
			}
		}
	}
	if len(candidates) == 0 {
		return
	}
	for i := range obs {
		o := &obs[i]
		b := o.ResponseBody
		if b.Kind != model.BodyOpaque || !textMedia[b.Media] || b.Size > ix.opts.MaxTextBytes {
			continue
		}
		data, ok := text(o)
		if !ok {
			continue
		}
		fam := fams.ByObservation[o.Ref().Key()]
		for _, tok := range tokens(data) {
			if !candidates[tok] {
				continue
			}
			// Found in text, the value is text, whatever type it had where it
			// was parsed.
			ix.add(tok, model.TypeString, ix.weak[tok], model.Occurrence{
				Ref: o.Ref(), FamilyID: fam,
				Loc: model.Location{Side: model.SideResponse, Part: model.PartText},
			})
		}
	}
}

// tokens returns the distinct tokens of a text body in first-seen order. A
// token is a run of letters, digits, underscores and hyphens, which is how
// identifiers sit inside URLs, attributes and script literals.
func tokens(data []byte) []string {
	seen := map[string]bool{}
	var out []string
	start := -1
	flush := func(end int) {
		if start >= 0 {
			tok := string(data[start:end])
			if !seen[tok] {
				seen[tok] = true
				out = append(out, tok)
			}
			start = -1
		}
	}
	for i, c := range data {
		if c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
	}
	flush(len(data))
	return out
}

func sortOccurrences(list []model.Occurrence) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Ref != b.Ref {
			return a.Ref.Less(b.Ref)
		}
		if a.Loc.Side != b.Loc.Side {
			// A response is read after its request.
			return a.Loc.Side == model.SideRequest
		}
		return a.Loc.String()+a.Loc.Path < b.Loc.String()+b.Loc.Path
	})
}

// Values returns every indexed value, sorted.
func (ix *Index) Values() []string {
	out := make([]string, 0, len(ix.occ))
	for v := range ix.occ {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Occurrences returns where a value was seen, in reference order with a
// request before its response.
func (ix *Index) Occurrences(value string) []model.Occurrence { return ix.occ[value] }

// Flags returns the flags of a value, sorted.
func (ix *Index) Flags(value string) []string {
	var out []string
	if ix.weak[value] {
		out = append(out, model.FlagLowInformation)
	}
	if len(ix.family[value]) > ix.opts.UbiquitousFamilies {
		out = append(out, model.FlagUbiquitous)
	}
	return out
}

// Type returns a value's scalar type, or the sorted types joined by "|"
// when it was seen as more than one, such as a query string "24" and a JSON
// number 24.
func (ix *Index) Type(value string) string {
	var ts []string
	for t := range ix.types[value] {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	return strings.Join(ts, "|")
}

// Links returns a link for every value seen in at least two families,
// ordered by id.
func (ix *Index) Links() []model.ValueLink {
	var out []model.ValueLink
	for _, v := range ix.Values() {
		if len(ix.family[v]) < 2 {
			continue
		}
		out = append(out, ix.link(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (ix *Index) link(value string) model.ValueLink {
	l := model.ValueLink{
		ID:        "vl_" + model.Hash("link", value),
		Value:     value,
		ValueType: ix.Type(value),
		Flags:     ix.Flags(value),
	}
	for f := range ix.family[value] {
		l.FamilyIDs = append(l.FamilyIDs, f)
	}
	sort.Strings(l.FamilyIDs)

	type key struct {
		family string
		loc    model.Location
	}
	byKey := map[key]*model.ValueOccurrence{}
	var order []key
	for _, o := range ix.occ[value] {
		// Concrete array indices are dropped so rows of one array aggregate.
		loc := o.Loc
		loc.Path = ""
		k := key{o.FamilyID, loc}
		vo := byKey[k]
		if vo == nil {
			vo = &model.ValueOccurrence{FamilyID: o.FamilyID, Location: loc}
			byKey[k] = vo
			order = append(order, k)
		}
		vo.Count++
		l.Count++
		if len(vo.Examples) < model.MaxVariantExamples && (len(vo.Examples) == 0 || vo.Examples[len(vo.Examples)-1] != o.Ref) {
			vo.Examples = append(vo.Examples, o.Ref)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.family != b.family {
			return a.family < b.family
		}
		return a.loc.String() < b.loc.String()
	})
	for _, k := range order {
		l.Locations = append(l.Locations, *byKey[k])
	}
	return l
}
