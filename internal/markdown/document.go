package markdown

import (
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Document is a converted page with its header fields.
type Document struct {
	URL         string
	Title       string
	Description string
	Captured    time.Time // zero when unknown
	Tags        []string  // the site's own labels
	Keywords    []string  // picked by TF-IDF across the corpus
	Body        string
}

// header is the YAML front matter, in field order.
type header struct {
	URL         string   `yaml:"url"`
	Title       string   `yaml:"title,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Captured    string   `yaml:"captured,omitempty"`
	Tags        []string `yaml:"tags,flow,omitempty"`
	Keywords    []string `yaml:"keywords,flow,omitempty"`
}

// capturedLayout keeps the zone abbreviation the capture recorded, since a
// proxy log's local zone, such as IDT, has no offset Go can look up.
const capturedLayout = "2006-01-02 15:04:05 MST"

// Render returns the document as Markdown with YAML front matter.
func (d *Document) Render() string {
	return d.RenderBody(d.Body)
}

// RenderBody renders the document's header over a replacement body, such
// as one whose links were rewritten.
func (d *Document) RenderBody(body string) string {
	h := header{URL: d.URL, Title: d.Title, Description: d.Description, Tags: d.Tags, Keywords: d.Keywords}
	if !d.Captured.IsZero() {
		h.Captured = d.Captured.Format(capturedLayout)
	}
	y, err := yaml.Marshal(h)
	if err != nil {
		// header holds only strings, which always marshal
		panic(err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(y)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}

// DefaultKeywords is how many keywords AddKeywords gives each document.
const DefaultKeywords = 8

// AddKeywords sets each document's Keywords by TF-IDF across docs, leaving
// out words already among its tags.
func AddKeywords(docs []*Document, k int) {
	corpus := make([][]string, len(docs))
	for i, d := range docs {
		corpus[i] = Tokens(d.Title + "\n" + d.Body)
	}
	for i, kws := range Keywords(corpus, k+maxSiteTags) {
		tags := map[string]bool{}
		for _, t := range docs[i].Tags {
			tags[strings.ToLower(t)] = true
		}
		docs[i].Keywords = nil
		for _, w := range kws {
			if !tags[w] && len(docs[i].Keywords) < k {
				docs[i].Keywords = append(docs[i].Keywords, w)
			}
		}
	}
}
