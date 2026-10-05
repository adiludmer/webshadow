# geektime-burp-browse

Real capture of a short browsing session on www.geektime.co.il (October
2026), recorded through the Burp Suite proxy rather than Chrome DevTools.
The home page and three articles were opened. Burp keeps every response
body, so unlike a DevTools HAR the article HTML is all here.

Built with:

```
webshadow bench import-burp burp-items.xml --out capture.raw.har
webshadow bench sanitize capture.raw.har --out session.har \
  --drop media,font,telemetry,stylesheet,script
```

The export held 39 items, all `text/html` (the Burp history filter was set
to HTML), so no JSON API responses are included. The raw export is not
committed.

This scenario has no goal (`evaluation: type: none`). It is for watching how
a generator handles full server-rendered pages; `make bench` stops after
the generate stage.
