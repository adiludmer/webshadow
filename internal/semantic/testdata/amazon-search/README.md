# amazon-search

`webshadow cluster` output for recording `rec_20261009_001`: an amazon.com
session of about 15 minutes with the homepage, about 15 keyword searches
(autocomplete, then `/s?k=`) and 5 product pages. 2,831 exchanges, 294
request families (151 static), 1,151 value links, 110 episodes, 53,042
sequence edges.

The files went through `webshadow analyze redact -gz` before they were
committed: every cookie, Set-Cookie, credential header, JWT and email
address is a `{{redacted:<location>:<hash>}}` tag. Equal values share a tag,
so value links and flows still join. `TestFixtureHoldsNoSecrets` checks
that loading it finds nothing left to redact.
