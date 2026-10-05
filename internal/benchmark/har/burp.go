package har

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// burpTimeLayout is the format of Burp's <time> element, for example
// "Mon Oct 05 07:14:42 IDT 2026".
const burpTimeLayout = "Mon Jan 02 15:04:05 MST 2006"

type burpItem struct {
	Time     string      `xml:"time"`
	URL      string      `xml:"url"`
	Method   string      `xml:"method"`
	Request  burpMessage `xml:"request"`
	Status   int         `xml:"status"`
	MIMEType string      `xml:"mimetype"`
	Response burpMessage `xml:"response"`
}

type burpMessage struct {
	Base64 bool   `xml:"base64,attr"`
	Data   string `xml:",chardata"`
}

func (m burpMessage) bytes() ([]byte, error) {
	if !m.Base64 {
		return []byte(m.Data), nil
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(m.Data))
}

// BurpReport counts what ConvertBurp did.
type BurpReport struct {
	Entries int
	// Undecoded counts response bodies left in a content encoding Go
	// cannot decode, such as Brotli; they are kept as base64.
	Undecoded int
}

// ConvertBurp reads a Burp Suite "Save items" XML export and writes it as a
// HAR 1.2 document, one entry per item in export order. Raw HTTP messages
// are split into headers and body; chunked and gzip or deflate bodies are
// decoded. The output is not sanitized: run it through Sanitize before it
// is stored anywhere.
func ConvertBurp(r io.Reader, w io.Writer) (BurpReport, error) {
	var rep BurpReport
	dec := xml.NewDecoder(r)
	dec.Strict = false // Burp's export carries a DTD; ignore it
	var entries []map[string]any
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return rep, fmt.Errorf("reading Burp XML: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "item" {
			continue
		}
		var item burpItem
		if err := dec.DecodeElement(&item, &start); err != nil {
			return rep, fmt.Errorf("item %d: %w", len(entries), err)
		}
		e, undecoded, err := burpEntry(item)
		if err != nil {
			return rep, fmt.Errorf("item %d (%s): %w", len(entries), item.URL, err)
		}
		if undecoded {
			rep.Undecoded++
		}
		entries = append(entries, e)
	}
	if entries == nil {
		return rep, fmt.Errorf("no <item> elements found; is this a Burp XML export?")
	}
	rep.Entries = len(entries)
	doc := map[string]any{"log": map[string]any{
		"version": "1.2",
		"creator": map[string]any{"name": "webshadow bench import-burp", "version": "1"},
		"entries": entries,
	}}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return rep, enc.Encode(doc)
}

func burpEntry(item burpItem) (map[string]any, bool, error) {
	u, err := url.Parse(strings.TrimSpace(item.URL))
	if err != nil {
		return nil, false, fmt.Errorf("bad url: %w", err)
	}
	started := time.Time{}
	if ts, err := time.Parse(burpTimeLayout, strings.TrimSpace(item.Time)); err == nil {
		started = ts
	}

	rawReq, err := item.Request.bytes()
	if err != nil {
		return nil, false, fmt.Errorf("request: %w", err)
	}
	reqLine, reqHeaders, reqBody := splitHTTP(rawReq)
	reqProto := "HTTP/1.1"
	if f := strings.Fields(reqLine); len(f) == 3 {
		reqProto = f[2]
	}
	request := map[string]any{
		"method":      strings.ToUpper(strings.TrimSpace(item.Method)),
		"url":         u.String(),
		"httpVersion": reqProto,
		"headers":     harHeaders(reqHeaders),
		"queryString": harQuery(u),
		"cookies":     []any{},
		"headersSize": -1,
		"bodySize":    len(reqBody),
	}
	if len(reqBody) > 0 {
		request["postData"] = map[string]any{"mimeType": headerValue(reqHeaders, "Content-Type"), "text": string(reqBody)}
	}

	rawResp, err := item.Response.bytes()
	if err != nil {
		return nil, false, fmt.Errorf("response: %w", err)
	}
	respLine, respHeaders, body := splitHTTP(rawResp)
	respProto, statusText := "HTTP/1.1", ""
	if f := strings.SplitN(respLine, " ", 3); len(f) >= 2 {
		respProto = f[0]
		if len(f) == 3 {
			statusText = f[2]
		}
	}
	if strings.EqualFold(headerValue(respHeaders, "Transfer-Encoding"), "chunked") {
		if b, err := io.ReadAll(httputil.NewChunkedReader(bytes.NewReader(body))); err == nil {
			body = b
		}
	}
	undecoded := false
	switch ce := strings.ToLower(strings.TrimSpace(headerValue(respHeaders, "Content-Encoding"))); ce {
	case "", "identity":
	case "gzip", "deflate":
		if b, err := decompress(ce, body); err == nil {
			body = b
		} else {
			undecoded = true
		}
	default:
		undecoded = true
	}
	mime := headerValue(respHeaders, "Content-Type")
	content := map[string]any{"size": len(body), "mimeType": mime}
	switch {
	case len(body) == 0:
	case !undecoded && utf8.Valid(body):
		content["text"] = string(body)
	default:
		content["text"] = base64.StdEncoding.EncodeToString(body)
		content["encoding"] = "base64"
	}
	response := map[string]any{
		"status":      item.Status,
		"statusText":  statusText,
		"httpVersion": respProto,
		"headers":     harHeaders(respHeaders),
		"cookies":     []any{},
		"content":     content,
		"redirectURL": headerValue(respHeaders, "Location"),
		"headersSize": -1,
		"bodySize":    len(body),
	}
	if len(rawResp) == 0 {
		response["status"] = 0
	}

	entry := map[string]any{
		"request":  request,
		"response": response,
		"cache":    map[string]any{},
		"time":     0,
		"timings":  map[string]any{"send": 0, "wait": 0, "receive": 0},
	}
	if !started.IsZero() {
		entry["startedDateTime"] = started.UTC().Format(time.RFC3339Nano)
	}
	return entry, undecoded, nil
}

// splitHTTP splits a raw HTTP message into its start line, headers and
// body. A message with no blank line is all headers.
func splitHTTP(raw []byte) (string, []Header, []byte) {
	if len(raw) == 0 {
		return "", nil, nil
	}
	head, body := raw, []byte(nil)
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		head, body = raw[:i], raw[i+4:]
	} else if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		head, body = raw[:i], raw[i+2:]
	}
	lines := strings.Split(strings.ReplaceAll(string(head), "\r\n", "\n"), "\n")
	var hs []Header
	for _, line := range lines[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		hs = append(hs, Header{textproto.TrimString(name), textproto.TrimString(value)})
	}
	return lines[0], hs, body
}

func headerValue(hs []Header, name string) string { return findHeader(hs, name) }

func harHeaders(hs []Header) []map[string]string {
	out := make([]map[string]string, len(hs))
	for i, h := range hs {
		out[i] = map[string]string{"name": h.Name, "value": h.Value}
	}
	return out
}

func harQuery(u *url.URL) []map[string]string {
	out := []map[string]string{}
	for _, pair := range strings.Split(u.RawQuery, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		if uk, err := url.QueryUnescape(k); err == nil {
			k = uk
		}
		if uv, err := url.QueryUnescape(v); err == nil {
			v = uv
		}
		out = append(out, map[string]string{"name": k, "value": v})
	}
	return out
}

func decompress(encoding string, body []byte) ([]byte, error) {
	var r io.Reader
	if encoding == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r = zr
	} else {
		r = flate.NewReader(bytes.NewReader(body))
	}
	return io.ReadAll(r)
}
