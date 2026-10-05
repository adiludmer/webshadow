package har

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"strings"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func gzipped(s string) string {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write([]byte(s))
	zw.Close()
	return b.String()
}

func TestConvertBurp(t *testing.T) {
	page := "<html><body>שלום, Enso raised $15M</body></html>"
	xml := `<?xml version="1.0"?>
<!DOCTYPE items [
<!ELEMENT items (item*)>
]>
<items burpVersion="2026.8" exportTime="Mon Oct 05 07:16:08 IDT 2026">
  <item>
    <time>Mon Oct 05 07:14:42 UTC 2026</time>
    <url><![CDATA[https://news.test/a?x=1&y=%D7%90]]></url>
    <method><![CDATA[GET]]></method>
    <request base64="true"><![CDATA[` + b64("GET /a?x=1 HTTP/2\r\nHost: news.test\r\nCookie: sid=SECRET\r\n\r\n") + `]]></request>
    <status>200</status>
    <mimetype>HTML</mimetype>
    <response base64="true"><![CDATA[` + b64("HTTP/2 200 OK\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Encoding: gzip\r\n\r\n"+gzipped(page)) + `]]></response>
  </item>
  <item>
    <time>bad time</time>
    <url><![CDATA[https://news.test/api]]></url>
    <method><![CDATA[POST]]></method>
    <request base64="true"><![CDATA[` + b64("POST /api HTTP/1.1\r\nContent-Type: application/json\r\n\r\n{\"q\":1}") + `]]></request>
    <status>200</status>
    <mimetype>JSON</mimetype>
    <response base64="true"><![CDATA[` + b64("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nContent-Type: application/json\r\n\r\n4\r\n{\"a\"\r\n3\r\n:1}\r\n0\r\n\r\n") + `]]></response>
  </item>
  <item>
    <time>Mon Oct 05 07:14:44 UTC 2026</time>
    <url><![CDATA[https://news.test/br]]></url>
    <method><![CDATA[GET]]></method>
    <request base64="false"><![CDATA[GET /br HTTP/1.1
Host: news.test

]]></request>
    <status>200</status>
    <mimetype></mimetype>
    <response base64="true"><![CDATA[` + b64("HTTP/1.1 200 OK\r\nContent-Encoding: br\r\n\r\n\x8b\x01\x80hi\x03") + `]]></response>
  </item>
</items>`
	var out bytes.Buffer
	rep, err := ConvertBurp(strings.NewReader(xml), &out)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Entries != 3 || rep.Undecoded != 1 {
		t.Errorf("report %+v", rep)
	}
	trace, err := Parse(&out)
	if err != nil {
		t.Fatalf("output is not a HAR the benchmark reads: %v", err)
	}
	e := trace.Entries[0]
	if e.Method != "GET" || e.URL != "https://news.test/a?x=1&y=%D7%90" || e.Status != 200 || string(e.ResponseBody) != page {
		t.Errorf("entry 0: %s %s %d %q", e.Method, e.URL, e.Status, e.ResponseBody)
	}
	if e.RequestHeader("Cookie") != "sid=SECRET" || e.ResponseMIME != "text/html; charset=UTF-8" || e.StartedAt.IsZero() {
		t.Errorf("entry 0 headers: cookie %q, mime %q, time %v", e.RequestHeader("Cookie"), e.ResponseMIME, e.StartedAt)
	}
	if len(Unsanitized(trace)) == 0 {
		t.Error("a converted capture with a Cookie header counts as sanitized")
	}
	e = trace.Entries[1]
	if string(e.RequestBody) != `{"q":1}` || e.RequestMIME != "application/json" || string(e.ResponseBody) != `{"a":1}` || !e.StartedAt.IsZero() {
		t.Errorf("entry 1: req %q (%s), resp %q, time %v", e.RequestBody, e.RequestMIME, e.ResponseBody, e.StartedAt)
	}
	if e = trace.Entries[2]; string(e.ResponseBody) != "\x8b\x01\x80hi\x03" {
		t.Errorf("entry 2 kept body %q", e.ResponseBody)
	}

	if _, err := ConvertBurp(strings.NewReader("<items></items>"), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no <item> elements") {
		t.Errorf("empty export: %v", err)
	}
}
