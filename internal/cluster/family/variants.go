package family

import (
	"sort"
	"strconv"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/shape"
)

// variants groups a family's responses by status, media type and structural
// shape. A success, an empty result and an error from one operation become
// three variants of one family rather than three families.
//
// An opaque response is grouped by media type alone and its size classes
// merged, so an HTML page that is sometimes larger is still one variant.
func variants(members []member) []model.ResponseVariant {
	byKey := map[string]*model.ResponseVariant{}
	for _, m := range members {
		o := m.obs
		key := variantKey(o)
		v := byKey[key]
		if v == nil {
			v = &model.ResponseVariant{
				ID:     model.Hash("variant", key),
				Status: o.Status,
				Media:  o.ResponseBody.Media,
				Shape:  o.ResponseBody.Shape,
			}
			if o.Error != "" {
				v.Error = "transport error"
			}
			byKey[key] = v
		} else {
			v.Shape = shape.Merge(v.Shape, o.ResponseBody.Shape)
		}
		v.Count++
		// Members arrive in reference order, so the first few are the
		// deterministic examples.
		if len(v.Examples) < model.MaxVariantExamples {
			v.Examples = append(v.Examples, o.Ref())
		}
	}
	out := make([]model.ResponseVariant, 0, len(byKey))
	for _, v := range byKey {
		out = append(out, *v)
	}
	// Most common first, so the usual behaviour leads; ties by id.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func variantKey(o *model.Observation) string {
	if o.Error != "" {
		// The error text carries addresses and timings; the fact of a
		// transport failure is the structure.
		return "error"
	}
	body := o.ResponseBody.Kind + ":"
	if o.ResponseBody.Kind == model.BodyOpaque {
		body += o.ResponseBody.Media
	} else {
		body += o.ResponseBody.Shape.Canonical()
	}
	return strconv.Itoa(o.Status) + " " + o.ResponseBody.Media + " " + body
}
