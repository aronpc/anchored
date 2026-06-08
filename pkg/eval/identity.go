package eval

import (
	"fmt"

	"github.com/jholhewres/anchored/pkg/project"
)

// RunIdentity asserts the project-identity invariants: a git origin derives a
// stable canonical (and legacy) remote key, equivalent URL forms (scp-syntax vs
// https) of the same repo resolve to the same canonical key, and distinct repos
// never collide. This is the pure-function core of "no silent project
// fallback" — if two repos collided on a key, sync could land memories in the
// wrong project.
func RunIdentity(fixture []byte) (Report, error) {
	var fix IdentityFixture
	if err := parseYAML(fixture, &fix); err != nil {
		return Report{}, err
	}

	canonical := make(map[string]string, len(fix.Cases))
	for _, c := range fix.Cases {
		canonical[c.Name] = project.DeriveRemoteKeyFromURL(c.Origin)
	}

	rep := Report{Name: "identity", Passed: true}
	var passCount float64
	for _, c := range fix.Cases {
		got := canonical[c.Name]
		gotLegacy := project.DeriveLegacyRemoteKeyFromURL(c.Origin)

		var problems []string
		if c.ExpectCanonical != "" && got != c.ExpectCanonical {
			problems = append(problems, fmt.Sprintf("canonical=%q want %q", got, c.ExpectCanonical))
		}
		if c.ExpectLegacy != "" && gotLegacy != c.ExpectLegacy {
			problems = append(problems, fmt.Sprintf("legacy=%q want %q", gotLegacy, c.ExpectLegacy))
		}
		if c.SameAs != "" {
			if other, ok := canonical[c.SameAs]; !ok {
				problems = append(problems, fmt.Sprintf("same_as references unknown case %q", c.SameAs))
			} else if other != got {
				problems = append(problems, fmt.Sprintf("canonical %q must equal %q (%s)", got, other, c.SameAs))
			}
		}
		if c.MustDiffer != "" {
			if other, ok := canonical[c.MustDiffer]; ok && other == got && got != "" {
				problems = append(problems, fmt.Sprintf("canonical %q must differ from %q (%s)", got, other, c.MustDiffer))
			}
		}
		passed := len(problems) == 0
		if !passed {
			rep.Passed = false
		} else {
			passCount++
		}
		rep.Cases = append(rep.Cases, CaseResult{
			Name:   c.Name,
			Passed: passed,
			Detail: detailOrOK(problems, fmt.Sprintf("canonical=%q legacy=%q", got, gotLegacy)),
		})
	}
	if len(rep.Cases) > 0 {
		rep.Score = passCount / float64(len(rep.Cases))
	}
	rep.Summary = fmt.Sprintf("%d identity cases, %d ok", len(rep.Cases), int(passCount))
	return rep, nil
}
