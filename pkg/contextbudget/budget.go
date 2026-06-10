package contextbudget

import (
	"sort"
	"strings"
)

// Item is a pre-rendered context block.
type Item struct {
	Text     string   // pre-rendered block (caller is responsible for escaping)
	Priority int      // lower = more important within the tier
	Signals  []string // explainability annotations (optional)
}

// Tier is a named group of items with a minimum-item guarantee.
type Tier struct {
	Name     string
	Items    []Item
	MinItems int // number of items guaranteed before lower tiers consume budget
}

// Assemble assembles tiers into a single string within budgetBytes.
//
// Rules:
//   - Items within each tier are sorted by Priority ascending (stable).
//   - Pass 1 (reserve): for each tier in order, include up to MinItems items
//     if they fit in the remaining budget.
//   - Pass 2 (fill): for each tier in order, include remaining items that fit.
//   - An item is never split: items that do not fit are dropped whole (counted in dropped).
//   - A higher tier never loses an item in favour of a lower tier.
//   - Items with empty Text are ignored (not counted as dropped).
//   - Deterministic: same input → same output.
//   - Output: blocks concatenated with "\n" in tier→priority order, plus total dropped.
//
// budgetBytes <= 0 returns ("", total non-empty items).
func Assemble(tiers []Tier, budgetBytes int) (out string, dropped int) {
	// Build sorted copies of each tier's non-empty items.
	type tierItems struct {
		items    []Item
		minItems int
	}
	sorted := make([]tierItems, len(tiers))
	totalNonEmpty := 0
	for i, t := range tiers {
		var items []Item
		for _, it := range t.Items {
			if it.Text != "" {
				items = append(items, it)
				totalNonEmpty++
			}
		}
		// Stable sort by Priority ascending.
		sort.SliceStable(items, func(a, b int) bool {
			return items[a].Priority < items[b].Priority
		})
		sorted[i] = tierItems{items: items, minItems: t.MinItems}
	}

	if budgetBytes <= 0 {
		return "", totalNonEmpty
	}

	included := make([][]bool, len(sorted))
	for i, ti := range sorted {
		included[i] = make([]bool, len(ti.items))
	}

	remaining := budgetBytes

	// costOf returns the byte cost of adding text to the output so far.
	// The separator "\n" is added before every item except the very first.
	hasAny := false
	cost := func(text string) int {
		if hasAny {
			return len("\n") + len(text)
		}
		return len(text)
	}

	// Pass 1: reserve MinItems for each tier.
	for i, ti := range sorted {
		reserved := 0
		for j, it := range ti.items {
			if reserved >= ti.minItems {
				break
			}
			c := cost(it.Text)
			if c <= remaining {
				included[i][j] = true
				remaining -= c
				if !hasAny {
					hasAny = true
				}
				reserved++
			}
			// If the item doesn't fit, we don't skip MinItems — we simply can't
			// guarantee it. Continue trying subsequent items to fill the minimum.
		}
	}

	// Pass 2: fill remaining items in tier→priority order.
	for i, ti := range sorted {
		for j, it := range ti.items {
			if included[i][j] {
				continue
			}
			c := cost(it.Text)
			if c <= remaining {
				included[i][j] = true
				remaining -= c
				if !hasAny {
					hasAny = true
				}
			}
		}
	}

	// Build output in tier→priority order; count dropped.
	var sb strings.Builder
	first := true
	for i, ti := range sorted {
		for j, it := range ti.items {
			if included[i][j] {
				if !first {
					sb.WriteByte('\n')
				}
				sb.WriteString(it.Text)
				first = false
			} else {
				dropped++
			}
		}
	}

	return sb.String(), dropped
}
