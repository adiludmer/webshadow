// Package family groups observations into request families. Construction is
// progressive: method, host and exact path first, then the inferred route
// template, then the query shape and the request body shape. Responses never
// split a family; they are attached as variants.
//
// A family's id hashes only its canonical structure and the clustering
// version, so the same operation found in two separate recordings gets the
// same id, and the order observations arrive in changes nothing.
package family

import (
	"sort"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/route"
)

// Result is the families built from a set of observations.
type Result struct {
	// Families are ordered by host, path template, method, then id.
	Families []model.RequestFamily
	// ByObservation maps an observation's reference key to its family id.
	ByObservation map[string]string
}

// Build groups observations into families. The observations may come from
// any number of recordings and in any order.
func Build(obs []model.Observation) Result {
	paths := make([]route.Path, 0, len(obs))
	for i := range obs {
		paths = append(paths, route.Path{Method: obs[i].Method, Host: obs[i].Host, Segments: obs[i].PathSegments})
	}
	router := route.Infer(paths)

	groups := map[string]*group{}
	byObs := make(map[string]string, len(obs))
	for i := range obs {
		o := &obs[i]
		tmpl := router.Match(o.Method, o.Host, o.PathSegments)
		key := identity(o, tmpl)
		g := groups[key]
		if g == nil {
			g = &group{key: key, tmpl: tmpl}
			groups[key] = g
		}
		g.members = append(g.members, member{obs: o, tmpl: tmpl})
	}

	res := Result{ByObservation: byObs}
	for _, g := range groups {
		f := g.family()
		for _, m := range g.members {
			byObs[m.obs.Ref().Key()] = f.ID
		}
		res.Families = append(res.Families, f)
	}
	sort.Slice(res.Families, func(i, j int) bool {
		a, b := res.Families[i], res.Families[j]
		switch {
		case a.Host != b.Host:
			return a.Host < b.Host
		case a.PathTemplate != b.PathTemplate:
			return a.PathTemplate < b.PathTemplate
		case a.Method != b.Method:
			return a.Method < b.Method
		}
		return a.ID < b.ID
	})
	return res
}

// identity is the canonical string a family id hashes. The request body
// enters as its structure; an opaque body enters by media type alone, since
// its size class would split one operation into a family per payload size.
func identity(o *model.Observation, tmpl route.Template) string {
	var b strings.Builder
	b.WriteString(routeIdentity(o.Method, o.Host, tmpl))
	b.WriteString(" ?")
	b.WriteString(o.QueryShape.Canonical())
	b.WriteString(" body:")
	b.WriteString(o.RequestBody.Kind)
	b.WriteByte(':')
	b.WriteString(bodyIdentity(o.RequestBody))
	return b.String()
}

func routeIdentity(method, host string, tmpl route.Template) string {
	return method + " " + host + " " + tmpl.Canonical()
}

func bodyIdentity(b model.Body) string {
	switch b.Kind {
	case model.BodyNone:
		return ""
	case model.BodyOpaque:
		return b.Media
	}
	return b.Shape.Canonical()
}

type member struct {
	obs  *model.Observation
	tmpl route.Template
}

type group struct {
	key     string
	tmpl    route.Template
	members []member
}

func (g *group) family() model.RequestFamily {
	// Members are put in reference order first, so every derived list is
	// independent of the order observations were passed in.
	sort.Slice(g.members, func(i, j int) bool { return g.members[i].obs.Ref().Less(g.members[j].obs.Ref()) })
	first := g.members[0].obs

	f := model.RequestFamily{
		ID:           model.Hash("family", g.key),
		Method:       first.Method,
		Host:         first.Host,
		PathTemplate: g.tmpl.Display(),
		Route:        g.tmpl.Segments,
		RouteID:      model.Hash("route", routeIdentity(first.Method, first.Host, g.tmpl)),
		QueryShape:   first.QueryShape,
		BodyKind:     first.RequestBody.Kind,
		RequestBody:  requestBodyShape(g.members),
		Static:       true,
	}
	for _, m := range g.members {
		f.Observations = append(f.Observations, m.obs.Ref())
		if !m.obs.Static() {
			f.Static = false
		}
	}
	f.Slots = slots(g)
	f.ResponseVariants = variants(g.members)
	return f
}

// requestBodyShape is the members' shared request body shape. Members agree
// on it by construction except for an opaque body's size class, which is
// cleared when it varies.
func requestBodyShape(members []member) model.Shape {
	s := members[0].obs.RequestBody.Shape
	for _, m := range members[1:] {
		if m.obs.RequestBody.Shape.SizeClass != s.SizeClass {
			s.SizeClass = ""
		}
	}
	return s
}
