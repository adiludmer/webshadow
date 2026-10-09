package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Reference prefixes. An evidence reference names something the clustering
// pass wrote; a node reference names something in the IR. Every reference
// is "<prefix>:<id>", so its kind is visible without a lookup.
const (
	// Evidence from initiative 002.
	RefFamily      = "fam"
	RefVariant     = "var" // var:<family id>/<variant id>
	RefValueLink   = "vl"
	RefSequence    = "se"
	RefEpisode     = "ep"
	RefTrace       = "tr"
	RefNavigation  = "nav"
	RefObservation = "ex" // ex:<session id>/<exchange id>

	// IR nodes.
	RefEntity       = "ent"
	RefField        = "fld"
	RefRelation     = "rel"
	RefOperation    = "op"
	RefPrerequisite = "pre"
	RefHypothesis   = "hyp"
)

// EvidencePrefixes lists the prefixes that point into clustering output.
var EvidencePrefixes = []string{
	RefFamily, RefVariant, RefValueLink, RefSequence, RefEpisode, RefTrace, RefNavigation, RefObservation,
}

// NodePrefixes lists the prefixes that point at IR nodes.
var NodePrefixes = []string{
	RefEntity, RefField, RefRelation, RefOperation, RefPrerequisite, RefHypothesis,
}

// Ref joins a prefix and an id.
func Ref(prefix string, parts ...string) string {
	return prefix + ":" + strings.Join(parts, "/")
}

// SplitRef returns a reference's prefix and id, or ok false when it has no
// known prefix.
func SplitRef(ref string) (prefix, id string, ok bool) {
	prefix, id, found := strings.Cut(ref, ":")
	if !found || id == "" {
		return "", "", false
	}
	for _, p := range EvidencePrefixes {
		if p == prefix {
			return prefix, id, true
		}
	}
	for _, p := range NodePrefixes {
		if p == prefix {
			return prefix, id, true
		}
	}
	return "", "", false
}

// IsNodeRef reports whether ref names an IR node rather than evidence.
func IsNodeRef(ref string) bool {
	prefix, _, ok := SplitRef(ref)
	if !ok {
		return false
	}
	for _, p := range NodePrefixes {
		if p == prefix {
			return true
		}
	}
	return false
}

// NodeID derives a stable IR node reference from the canonical form of
// what the node stands for, such as an entity's sorted identity paths. The
// same content always gets the same id, whatever the node is named.
func NodeID(prefix, canonical string) string {
	sum := sha256.Sum256([]byte("webshadow/semantic/" + SchemaVersion + "/" + prefix + "\x00" + canonical))
	return Ref(prefix, hex.EncodeToString(sum[:6]))
}
