# geektime-enso-funding

Real capture of a browsing session on www.geektime.co.il (captured October 2026),
sanitized and trimmed with:

```
webshadow bench sanitize www.geektime.co.il.har --out session.har \
  --drop media,font,telemetry,stylesheet,script --trim-initiators
```

The raw capture (210 MB, 2,676 requests) is not committed. The trimmed HAR
keeps the 197 first-party and third-party document and API requests;
images, fonts, scripts, stylesheets, analytics and reCAPTCHA are dropped.

The Enso article page (`/enso-ai-agents-growth-hacking-funding/`) was
captured with an empty body. The answer appears only in JSON feeds the site
loaded: `wp-content/uploads/hp.json`, `wp-json/app/v1/posts/more_posts` and a
Taboola recommendations response, in the headline
"סוכני ה-AI של enso גייסו 15 מיליון דולר" (Enso's AI agents raised $15 million).
A generator only gets this right if it reads API responses, not just pages.
