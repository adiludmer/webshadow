//go:build chromium_bundle

package chromium

import (
	_ "embed"
	"strings"
)

// The release build fetches these with scripts/fetch-chromium.sh before
// compiling; they are not committed.
var (
	//go:embed bundle/chromium.zip
	bundleZip []byte
	//go:embed bundle/REVISION
	bundleRevision string
)

func init() {
	bundled = &Bundle{Revision: strings.TrimSpace(bundleRevision), Zip: bundleZip}
}
