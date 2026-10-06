// Package shape derives structural shapes and scalar values from bodies and
// from query or form parameters. A shape keeps structure and drops values; a
// scalar keeps the value and where it sat. Both are needed: families are
// built from shapes, and value links from scalars.
package shape

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/model"
)

// Budget bounds the work one body may cost. A browser recording holds
// megabytes of JSON, and an unbounded walk over every array element of every
// response would dominate the run. Exceeding a budget stops the walk and
// marks the body truncated rather than dropping it.
type Budget struct {
	// MaxNodes is how many JSON values may be visited.
	MaxNodes int
	// MaxDepth is how deep the walk may go.
	MaxDepth int
	// MaxElems is how many elements of one array are visited; their shapes
	// still merge into the array's element shape.
	MaxElems int
}

// DefaultBudget is what the clustering pass uses. The numbers are part of
// the deterministic contract: changing them can change which values are
// indexed, so a change comes with a model.Version bump.
var DefaultBudget = Budget{MaxNodes: 20000, MaxDepth: 24, MaxElems: 200}

// Scalar is one value found inside a structure, with its position. Path
// carries concrete array indices ("products[4].asin") and Pattern collapses
// them ("products[].asin"), so occurrences in different rows aggregate.
type Scalar struct {
	Path    string
	Pattern string
	Type    string
	Value   string
}

// Result is what deriving a body yields.
type Result struct {
	Shape     model.Shape
	Scalars   []Scalar
	Truncated bool
}

// Opaque builds the shape of a body that was not parsed.
func Opaque(media string, size int64) model.Shape {
	return model.Shape{Type: model.TypeOpaque, Media: media, SizeClass: model.SizeClass(size)}
}

// OfJSON derives the shape and scalars of a JSON body. A body that does not
// parse returns an error; the caller records it as opaque with the reason,
// because a truncated capture is a fact about the recording, not about the
// site.
func OfJSON(data []byte, b Budget) (Result, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return Result{}, err
	}
	// Trailing content means this is not one JSON document; treating it as
	// one would invent a shape the server never sent.
	if dec.More() {
		return Result{}, errors.New("trailing data after JSON value")
	}
	w := &walker{budget: b}
	s := w.walk(v, "", "", 0)
	sortScalars(w.scalars)
	return Result{Shape: s, Scalars: w.scalars, Truncated: w.truncated}, nil
}

// OfForm derives the shape and scalars of an application/x-www-form-urlencoded
// body.
func OfForm(data []byte) (Result, error) {
	vals, err := url.ParseQuery(string(data))
	if err != nil {
		return Result{}, err
	}
	var pairs []model.Pair
	for name, list := range vals {
		for _, v := range list {
			pairs = append(pairs, model.Pair{Name: name, Value: v})
		}
	}
	sortPairs(pairs)
	res := Result{Shape: OfPairs(pairs)}
	res.Scalars = scalarsOfPairs(pairs)
	return res, nil
}

// OfMultipart derives the shape and scalars of a multipart body. File parts
// contribute an opaque shape and no scalar, since their bytes are content
// rather than a value another request could echo.
func OfMultipart(data []byte, boundary string) (Result, error) {
	if boundary == "" {
		return Result{}, errors.New("multipart body without a boundary")
	}
	r := multipart.NewReader(bytes.NewReader(data), boundary)
	var (
		fields  []model.Field
		scalars []Scalar
	)
	seen := map[string]bool{}
	for {
		part, err := r.NextPart()
		if err != nil {
			break
		}
		name := part.FormName()
		if name == "" {
			name = part.FileName()
		}
		var buf bytes.Buffer
		// A multipart file can be large; only enough is read to classify it.
		n, _ := buf.ReadFrom(io.LimitReader(part, 1<<20))
		part.Close()
		if seen[name] {
			continue
		}
		seen[name] = true
		if part.FileName() != "" {
			media, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			fields = append(fields, model.Field{Name: name, Shape: Opaque(media, n)})
			continue
		}
		fields = append(fields, model.Field{Name: name, Shape: model.Shape{Type: model.TypeString}})
		scalars = append(scalars, Scalar{Path: name, Pattern: name, Type: model.TypeString, Value: buf.String()})
	}
	model.SortFields(fields)
	sortScalars(scalars)
	return Result{Shape: model.Shape{Type: model.TypeMultipart, Fields: fields}, Scalars: scalars}, nil
}

// OfPairs builds the shape of a query string or form: one field per distinct
// key, sorted, a repeated key becoming an array. Values are always typed as
// strings even when they look numeric, so that page=2 and page=next do not
// split a family that is otherwise identical.
func OfPairs(pairs []model.Pair) model.Shape {
	if len(pairs) == 0 {
		return model.Shape{Type: model.TypeObject}
	}
	counts := map[string]int{}
	for _, p := range pairs {
		counts[p.Name]++
	}
	fields := make([]model.Field, 0, len(counts))
	for name, n := range counts {
		s := model.Shape{Type: model.TypeString}
		if n > 1 {
			s = model.Shape{Type: model.TypeArray, Elem: &model.Shape{Type: model.TypeString}}
		}
		fields = append(fields, model.Field{Name: name, Shape: s})
	}
	model.SortFields(fields)
	return model.Shape{Type: model.TypeObject, Fields: fields}
}

// ScalarsOfPairs returns the values of query or form pairs as scalars. A
// repeated key keeps its index in Path, so two values of one key stay
// distinguishable, while Pattern is the bare key.
func ScalarsOfPairs(pairs []model.Pair) []Scalar { return scalarsOfPairs(pairs) }

func scalarsOfPairs(pairs []model.Pair) []Scalar {
	n := map[string]int{}
	out := make([]Scalar, 0, len(pairs))
	for _, p := range pairs {
		path := p.Name
		if n[p.Name] > 0 {
			path = fmt.Sprintf("%s[%d]", p.Name, n[p.Name])
		}
		n[p.Name]++
		out = append(out, Scalar{Path: path, Pattern: p.Name, Type: model.TypeString, Value: p.Value})
	}
	sortScalars(out)
	return out
}

// Merge combines two shapes into the one that describes both. Conflicting
// types become Any rather than splitting; an object keeps the union of its
// fields, since a field missing from one observation is optional, not
// different. Field-level presence counts live on the family, not here.
func Merge(a, b model.Shape) model.Shape {
	switch {
	case a.Type == model.TypeAbsent || a.Type == "":
		return b
	case b.Type == model.TypeAbsent || b.Type == "":
		return a
	}
	if a.Type != b.Type {
		// Integers are numbers; a field seen as 1 and as 1.5 is a number.
		if isNumeric(a.Type) && isNumeric(b.Type) {
			return model.Shape{Type: model.TypeNumber}
		}
		return model.Shape{Type: model.TypeAny}
	}
	switch a.Type {
	case model.TypeObject, model.TypeMultipart:
		fields := make([]model.Field, 0, len(a.Fields)+len(b.Fields))
		fields = append(fields, a.Fields...)
		for _, f := range b.Fields {
			if i := indexField(fields, f.Name); i >= 0 {
				fields[i].Shape = Merge(fields[i].Shape, f.Shape)
				continue
			}
			fields = append(fields, f)
		}
		model.SortFields(fields)
		return model.Shape{Type: a.Type, Fields: fields}
	case model.TypeArray:
		switch {
		case a.Elem == nil:
			return b
		case b.Elem == nil:
			return a
		}
		elem := Merge(*a.Elem, *b.Elem)
		return model.Shape{Type: model.TypeArray, Elem: &elem}
	case model.TypeOpaque:
		out := model.Shape{Type: model.TypeOpaque, Media: a.Media, SizeClass: a.SizeClass}
		if a.Media != b.Media {
			out.Media = ""
		}
		if a.SizeClass != b.SizeClass {
			out.SizeClass = ""
		}
		return out
	}
	return a
}

func isNumeric(t string) bool { return t == model.TypeInteger || t == model.TypeNumber }

func indexField(fields []model.Field, name string) int {
	for i, f := range fields {
		if f.Name == name {
			return i
		}
	}
	return -1
}

// walker derives a shape and collects scalars in one pass over a decoded
// JSON value.
type walker struct {
	budget    Budget
	nodes     int
	scalars   []Scalar
	truncated bool
}

func (w *walker) walk(v any, path, pattern string, depth int) model.Shape {
	w.nodes++
	if w.nodes > w.budget.MaxNodes || depth > w.budget.MaxDepth {
		w.truncated = true
		return model.Shape{Type: model.TypeAny}
	}
	switch v := v.(type) {
	case nil:
		return model.Shape{Type: model.TypeNull}
	case bool:
		w.add(path, pattern, model.TypeBool, strconv.FormatBool(v))
		return model.Shape{Type: model.TypeBool}
	case json.Number:
		t := model.TypeNumber
		if _, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
			t = model.TypeInteger
		}
		w.add(path, pattern, t, v.String())
		return model.Shape{Type: t}
	case string:
		w.add(path, pattern, model.TypeString, v)
		return model.Shape{Type: model.TypeString}
	case []any:
		elem := model.Shape{Type: model.TypeAbsent}
		for i, item := range v {
			if i >= w.budget.MaxElems {
				w.truncated = true
				break
			}
			elem = Merge(elem, w.walk(item, fmt.Sprintf("%s[%d]", path, i), pattern+"[]", depth+1))
		}
		if elem.Type == model.TypeAbsent {
			return model.Shape{Type: model.TypeArray}
		}
		return model.Shape{Type: model.TypeArray, Elem: &elem}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		// Sorting here is what makes two bodies with the same keys in
		// different order hash alike.
		sort.Strings(keys)
		fields := make([]model.Field, 0, len(keys))
		for _, k := range keys {
			fields = append(fields, model.Field{Name: k, Shape: w.walk(v[k], join(path, k), join(pattern, k), depth+1)})
		}
		return model.Shape{Type: model.TypeObject, Fields: fields}
	default:
		return model.Shape{Type: model.TypeAny}
	}
}

func (w *walker) add(path, pattern, typ, value string) {
	// The root of a body is a value with no position; it is still indexed,
	// under the empty pattern, so a bare JSON string response can link.
	w.scalars = append(w.scalars, Scalar{Path: path, Pattern: pattern, Type: typ, Value: value})
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func sortScalars(s []Scalar) {
	sort.SliceStable(s, func(i, j int) bool {
		a, b := s[i], s[j]
		if a.Pattern != b.Pattern {
			return a.Pattern < b.Pattern
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Value < b.Value
	})
}

func sortPairs(p []model.Pair) {
	sort.SliceStable(p, func(i, j int) bool {
		if p[i].Name != p[j].Name {
			return p[i].Name < p[j].Name
		}
		return p[i].Value < p[j].Value
	})
}

// MediaType returns the bare media type of a Content-Type header, lowercased,
// plus its parameters.
func MediaType(contentType string) (string, map[string]string) {
	if contentType == "" {
		return "", nil
	}
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		// A malformed Content-Type still tells us something; keep the part
		// before any parameter.
		media = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
		return media, nil
	}
	return strings.ToLower(media), params
}

// Kind classifies a body by media type, which decides how it is parsed.
func Kind(media string) string {
	switch {
	case media == "":
		return model.BodyOpaque
	case media == "application/json", media == "text/json",
		strings.HasSuffix(media, "+json"):
		return model.BodyJSON
	case media == "application/x-www-form-urlencoded":
		return model.BodyForm
	case strings.HasPrefix(media, "multipart/"):
		return model.BodyMultipart
	default:
		return model.BodyOpaque
	}
}
