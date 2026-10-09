package semantic

import (
	"os/exec"
	"strings"
	"testing"
)

// Analysis must never reach the network: no package under semantic imports
// net/http or a dialer. (The clustering packages it reads results through
// use net/http only to parse captured messages.)
func TestAnalysisImportsNoNetwork(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}}: {{join .Imports \" \"}}", "./...").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v\n%s", err, out)
	}
	banned := map[string]bool{"net": true, "net/http": true, "net/rpc": true, "crypto/tls": true}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, _ := strings.Cut(line, ": ")
		for _, p := range strings.Fields(imports) {
			if banned[p] {
				t.Errorf("%s imports %s", pkg, p)
			}
		}
	}
}
