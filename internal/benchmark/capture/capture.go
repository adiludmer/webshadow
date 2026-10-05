// Package capture parses, classifies and sanitizes proxy captures for the
// benchmark. A capture is a Burp Suite "Save items" XML export: each item
// holds the raw HTTP request and response as they crossed the proxy, so
// every body is there, including pages a browser drops on navigation.
// Parsing produces a normalized Trace; sanitization rewrites the export
// itself so every downstream consumer sees the redacted version.
package capture

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Trace is the normalized view of a capture, one entry per item in file
// order.
type Trace struct {
	Entries []Entry
}

// Header is one HTTP header as recorded.
type Header struct {
	Name  string
	Value string
}

// Entry is one request/response pair.
type Entry struct {
	Sequence int // zero-based position in the capture
	Method   string
	URL      string
	Host     string
	Path     string
	Query    url.Values
	Status   int // 0 when the capture has no response
	Class    Class

	RequestHeaders  []Header
	RequestMIME     string
	RequestBody     []byte
	ResponseHeaders []Header
	ResponseMIME    string
	// ResponseBody is the decoded body: chunked transfer coding and gzip or
	// deflate content coding are undone. A body in a coding Go cannot
	// decode, such as Brotli, is kept as recorded.
	ResponseBody []byte
	// ResponseText is an HTML response rendered as readable text by
	// HTMLText; it is empty for every other response.
	ResponseText string

	StartedAt time.Time // zero when the item's time does not parse
}

// Readable returns what a reader should see of the response: the rendered
// text of an HTML page, or the body itself.
func (e *Entry) Readable() []byte {
	if e.ResponseText != "" {
		return []byte(e.ResponseText)
	}
	return e.ResponseBody
}

// RequestHeader returns the first request header with the given name,
// compared case-insensitively.
func (e *Entry) RequestHeader(name string) string { return findHeader(e.RequestHeaders, name) }

// ResponseHeader returns the first response header with the given name.
func (e *Entry) ResponseHeader(name string) string { return findHeader(e.ResponseHeaders, name) }

func findHeader(hs []Header, name string) string {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// timeLayout is the format of Burp's <time> element, for example
// "Mon Oct 05 07:14:42 IDT 2026".
const timeLayout = "Mon Jan 02 15:04:05 MST 2006"

// item mirrors one <item> of a Burp export. Every element is kept so a
// sanitized export can be written back in the same shape.
type item struct {
	XMLName        xml.Name `xml:"item"`
	Time           string   `xml:"time"`
	URL            cdata    `xml:"url"`
	Host           host     `xml:"host"`
	Port           string   `xml:"port"`
	Protocol       string   `xml:"protocol"`
	Method         cdata    `xml:"method"`
	Path           cdata    `xml:"path"`
	Extension      string   `xml:"extension"`
	Request        message  `xml:"request"`
	Status         string   `xml:"status"`
	ResponseLength string   `xml:"responselength"`
	MIMEType       string   `xml:"mimetype"`
	Response       message  `xml:"response"`
	Comment        string   `xml:"comment"`
}

type cdata struct {
	Text string `xml:",cdata"`
}

type host struct {
	IP   string `xml:"ip,attr"`
	Name string `xml:",chardata"`
}

// message is a raw HTTP message, base64-encoded when Base64 is set.
type message struct {
	Base64 bool   `xml:"base64,attr"`
	Data   string `xml:",cdata"`
}

func (m message) raw() ([]byte, error) {
	if !m.Base64 {
		return []byte(m.Data), nil
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(m.Data))
}

// export is a whole Burp XML export: the root's attributes and its items.
type export struct {
	attrs []xml.Attr
	items []item
}

// readExport decodes a Burp XML export.
func readExport(r io.Reader) (*export, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false // the export carries a DTD
	var exp *export
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid XML: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case start.Name.Local == "items" && exp == nil:
			exp = &export{attrs: start.Attr}
		case start.Name.Local == "item" && exp != nil:
			var it item
			if err := dec.DecodeElement(&it, &start); err != nil {
				return nil, fmt.Errorf("item %d: %w", len(exp.items), err)
			}
			exp.items = append(exp.items, it)
		}
	}
	if exp == nil {
		return nil, fmt.Errorf("no <items> element; expected a Burp Suite XML export")
	}
	return exp, nil
}

// exportHead is the prolog Burp writes before <items>.
const exportHead = `<?xml version="1.0"?>
<!DOCTYPE items [
<!ELEMENT items (item*)>
<!ATTLIST items burpVersion CDATA "">
<!ATTLIST items exportTime CDATA "">
<!ELEMENT item (time, url, host, port, protocol, method, path, extension, request, status, responselength, mimetype, response, comment)>
<!ELEMENT time (#PCDATA)>
<!ELEMENT url (#PCDATA)>
<!ELEMENT host (#PCDATA)>
<!ATTLIST host ip CDATA "">
<!ELEMENT port (#PCDATA)>
<!ELEMENT protocol (#PCDATA)>
<!ELEMENT method (#PCDATA)>
<!ELEMENT path (#PCDATA)>
<!ELEMENT extension (#PCDATA)>
<!ELEMENT request (#PCDATA)>
<!ATTLIST request base64 (true|false) "false">
<!ELEMENT status (#PCDATA)>
<!ELEMENT responselength (#PCDATA)>
<!ELEMENT mimetype (#PCDATA)>
<!ELEMENT response (#PCDATA)>
<!ATTLIST response base64 (true|false) "false">
<!ELEMENT comment (#PCDATA)>
]>
`

// write encodes the export in Burp's format, so Burp can load it again.
func (exp *export) write(w io.Writer) error {
	bw := bufio.NewWriter(w)
	bw.WriteString(exportHead)
	bw.WriteString("<items")
	for _, a := range exp.attrs {
		fmt.Fprintf(bw, " %s=\"", a.Name.Local)
		xml.EscapeText(bw, []byte(a.Value))
		bw.WriteString("\"")
	}
	bw.WriteString(">\n")
	enc := xml.NewEncoder(bw)
	enc.Indent("  ", "  ")
	for _, it := range exp.items {
		if err := enc.Encode(it); err != nil {
			return err
		}
		bw.WriteString("\n")
	}
	bw.WriteString("</items>\n")
	return bw.Flush()
}

// Parse reads a Burp XML export into a Trace. Errors carry no file name;
// callers add that context. An item whose URL or messages cannot be read
// is an error, since every later stage keys on host, path and bodies.
func Parse(r io.Reader) (*Trace, error) {
	exp, err := readExport(r)
	if err != nil {
		return nil, err
	}
	t := &Trace{Entries: make([]Entry, 0, len(exp.items))}
	for i, it := range exp.items {
		e, err := convert(i, it)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		t.Entries = append(t.Entries, e)
	}
	return t, nil
}

func convert(seq int, it item) (Entry, error) {
	rawURL := strings.TrimSpace(it.URL.Text)
	u, err := url.Parse(rawURL)
	if err != nil {
		return Entry{}, fmt.Errorf("bad url %q: %w", rawURL, err)
	}
	reqRaw, err := it.Request.raw()
	if err != nil {
		return Entry{}, fmt.Errorf("request is not valid base64: %w", err)
	}
	respRaw, err := it.Response.raw()
	if err != nil {
		return Entry{}, fmt.Errorf("response is not valid base64: %w", err)
	}
	req, resp := splitMessage(reqRaw), splitMessage(respRaw)
	status, _ := strconv.Atoi(strings.TrimSpace(it.Status))
	if len(respRaw) == 0 {
		status = 0
	}
	e := Entry{
		Sequence:        seq,
		Method:          strings.ToUpper(strings.TrimSpace(it.Method.Text)),
		URL:             rawURL,
		Host:            strings.ToLower(u.Host),
		Path:            u.EscapedPath(),
		Query:           u.Query(),
		Status:          status,
		RequestHeaders:  req.headers,
		RequestMIME:     findHeader(req.headers, "Content-Type"),
		RequestBody:     req.body,
		ResponseHeaders: resp.headers,
		ResponseMIME:    findHeader(resp.headers, "Content-Type"),
		ResponseBody:    decodeBody(resp.headers, resp.body),
	}
	if e.Path == "" {
		e.Path = "/"
	}
	if ts, err := time.Parse(timeLayout, strings.TrimSpace(it.Time)); err == nil {
		e.StartedAt = ts
	}
	e.Class = Classify(&e)
	if IsHTML(e.ResponseMIME) && utf8.Valid(e.ResponseBody) {
		e.ResponseText = HTMLText(e.ResponseBody, rawURL)
	}
	return e, nil
}

// httpMessage is a raw HTTP message split into its parts.
type httpMessage struct {
	start   string // request or status line
	headers []Header
	body    []byte
}

// splitMessage splits a raw HTTP message at the first blank line. A
// message with no blank line is all head.
func splitMessage(raw []byte) httpMessage {
	if len(raw) == 0 {
		return httpMessage{}
	}
	head, body := raw, []byte(nil)
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		head, body = raw[:i], raw[i+4:]
	} else if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		head, body = raw[:i], raw[i+2:]
	}
	lines := strings.Split(strings.ReplaceAll(string(head), "\r\n", "\n"), "\n")
	m := httpMessage{start: lines[0], body: body}
	for _, line := range lines[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		m.headers = append(m.headers, Header{textproto.TrimString(name), textproto.TrimString(value)})
	}
	return m
}

// bytes joins the message back into raw HTTP with CRLF line endings.
func (m httpMessage) bytes() []byte {
	if m.start == "" && len(m.headers) == 0 && len(m.body) == 0 {
		return nil
	}
	var b bytes.Buffer
	b.WriteString(m.start)
	b.WriteString("\r\n")
	for _, h := range m.headers {
		b.WriteString(h.Name)
		b.WriteString(": ")
		b.WriteString(h.Value)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	b.Write(m.body)
	return b.Bytes()
}

// decodeBody undoes chunked transfer coding and gzip or deflate content
// coding. A body that fails to decode is returned as recorded.
func decodeBody(hs []Header, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	if strings.EqualFold(strings.TrimSpace(findHeader(hs, "Transfer-Encoding")), "chunked") {
		if b, err := io.ReadAll(httputil.NewChunkedReader(bytes.NewReader(body))); err == nil {
			body = b
		}
	}
	var r io.Reader
	switch strings.ToLower(strings.TrimSpace(findHeader(hs, "Content-Encoding"))) {
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return body
		}
		r = zr
	case "deflate":
		r = flate.NewReader(bytes.NewReader(body))
	default:
		return body
	}
	if b, err := io.ReadAll(r); err == nil {
		return b
	}
	return body
}

// decodable reports whether decodeBody fully decodes a body with these
// headers, so the sanitizer can drop the coding headers after decoding.
func decodable(hs []Header) bool {
	switch strings.ToLower(strings.TrimSpace(findHeader(hs, "Content-Encoding"))) {
	case "", "identity", "gzip", "x-gzip", "deflate":
		return true
	}
	return false
}
