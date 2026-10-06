package chromium

// Bundle is the Chromium snapshot embedded in a release binary.
type Bundle struct {
	Revision string
	Zip      []byte
}

// bundled is set by bundle_embed.go in builds tagged chromium_bundle.
var bundled *Bundle

// Bundled reports the embedded Chromium, or nil in a development build.
func Bundled() *Bundle { return bundled }
