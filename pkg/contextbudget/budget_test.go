package contextbudget

import (
	"strings"
	"testing"
)

func TestAssemble(t *testing.T) {
	tests := []struct {
		name        string
		tiers       []Tier
		budget      int
		wantOut     string
		wantDropped int
	}{
		{
			name:        "budget zero returns empty and all non-empty items as dropped",
			tiers:       []Tier{{Name: "a", Items: []Item{{Text: "hello"}, {Text: "world"}}}},
			budget:      0,
			wantOut:     "",
			wantDropped: 2,
		},
		{
			name:        "budget negative returns empty and all non-empty items as dropped",
			tiers:       []Tier{{Name: "a", Items: []Item{{Text: "abc"}, {Text: "def"}}}},
			budget:      -1,
			wantOut:     "",
			wantDropped: 2,
		},
		{
			name: "empty text items ignored not counted as dropped",
			tiers: []Tier{{Name: "a", Items: []Item{
				{Text: ""},
				{Text: "hello"},
				{Text: ""},
			}}},
			budget:      100,
			wantOut:     "hello",
			wantDropped: 0,
		},
		{
			name: "single item fits exactly",
			tiers: []Tier{{Name: "a", Items: []Item{
				{Text: "12345"},
			}}},
			budget:      5,
			wantOut:     "12345",
			wantDropped: 0,
		},
		{
			name: "item larger than budget is dropped whole",
			tiers: []Tier{{Name: "a", Items: []Item{
				{Text: "123456"},
			}}},
			budget:      5,
			wantOut:     "",
			wantDropped: 1,
		},
		{
			name: "ceiling respected byte by byte with separator",
			// first item: 5 bytes, second item: 5 bytes + 1 sep = 6 bytes; total needed = 11
			// budget 10: second item must be dropped
			tiers: []Tier{{Name: "a", Items: []Item{
				{Text: "aaaaa", Priority: 1},
				{Text: "bbbbb", Priority: 2},
			}}},
			budget:      10,
			wantOut:     "aaaaa",
			wantDropped: 1,
		},
		{
			name: "ceiling respected byte by byte with separator fits exactly",
			// 5 + 1 + 5 = 11 bytes exactly
			tiers: []Tier{{Name: "a", Items: []Item{
				{Text: "aaaaa", Priority: 1},
				{Text: "bbbbb", Priority: 2},
			}}},
			budget:      11,
			wantOut:     "aaaaa\nbbbbb",
			wantDropped: 0,
		},
		{
			name: "priority order within tier",
			tiers: []Tier{{Name: "a", Items: []Item{
				{Text: "low", Priority: 10},
				{Text: "high", Priority: 1},
				{Text: "mid", Priority: 5},
			}}},
			budget:      100,
			wantOut:     "high\nmid\nlow",
			wantDropped: 0,
		},
		{
			name: "tier order preserved in output",
			tiers: []Tier{
				{Name: "first", Items: []Item{{Text: "A", Priority: 1}}},
				{Name: "second", Items: []Item{{Text: "B", Priority: 1}}},
				{Name: "third", Items: []Item{{Text: "C", Priority: 1}}},
			},
			budget:      100,
			wantOut:     "A\nB\nC",
			wantDropped: 0,
		},
		{
			name: "higher tier preserved when lower tier would exhaust budget",
			// Tier 0: 4 bytes; Tier 1: would need 1 sep + 4 = 5 bytes; budget = 9 = fits both
			// Tier 0: 4 bytes; Tier 1: would need 1 sep + 4 = 5 bytes; budget = 8 = fits only tier 0
			tiers: []Tier{
				{Name: "top", Items: []Item{{Text: "AAAA", Priority: 1}}},
				{Name: "bottom", Items: []Item{{Text: "BBBB", Priority: 1}}},
			},
			budget:      8,
			wantOut:     "AAAA",
			wantDropped: 1,
		},
		{
			// MinItems guarantee: tier A has 10 large items (50 bytes each), tier B MinItems=1.
			// Budget = 10 large items is impossible (10*50 + 9 seps = 509).
			// But tier B's first item should be reserved before tier A exhausts budget.
			name: "MinItems guarantees top item of lower tier even with large upper tier",
			tiers: func() []Tier {
				var aItems []Item
				// 10 items at 20 bytes each; total = 10*20 + 9 = 209 bytes
				for i := 0; i < 10; i++ {
					aItems = append(aItems, Item{Text: strings.Repeat("X", 20), Priority: i})
				}
				bItems := []Item{
					{Text: "BSMALL", Priority: 0}, // 6 bytes — the one that must survive
					{Text: strings.Repeat("Y", 20), Priority: 1},
				}
				return []Tier{
					{Name: "A", Items: aItems, MinItems: 0},
					{Name: "B", Items: bItems, MinItems: 1},
				}
			}(),
			// Budget = 20 (fits exactly 1 A item with no sep) + 1 (sep) + 6 (BSMALL) = 27
			// Pass 1: B reserves BSMALL (6 bytes). Remaining after: 27-6=21.
			// Pass 2: A fills with first item (20 bytes, no sep if BSMALL was placed first... wait)
			// Actually pass 1 runs in tier order, so A first (MinItems=0 → skip), then B reserves BSMALL.
			// After pass 1: hasAny=true (BSMALL included), remaining = 27-6 = 21.
			// Pass 2: A item 0: cost = 1+20 = 21 → fits. remaining=0.
			// Output order: tier A (item0=X*20), tier B (item0=BSMALL, item1 dropped).
			budget:      27,
			wantOut:     strings.Repeat("X", 20) + "\nBSMALL",
			wantDropped: 10, // 9 remaining A items + 1 remaining B item
		},
		{
			name: "deterministic same input same output",
			tiers: []Tier{
				{Name: "a", Items: []Item{
					{Text: "z", Priority: 3},
					{Text: "a", Priority: 1},
					{Text: "m", Priority: 2},
				}},
			},
			budget:      100,
			wantOut:     "a\nm\nz",
			wantDropped: 0,
		},
		{
			name:        "no tiers returns empty",
			tiers:       nil,
			budget:      100,
			wantOut:     "",
			wantDropped: 0,
		},
		{
			name: "all items empty",
			tiers: []Tier{{Name: "a", Items: []Item{
				{Text: ""},
				{Text: ""},
			}}},
			budget:      100,
			wantOut:     "",
			wantDropped: 0,
		},
		{
			name: "MinItems zero still fills on pass 2",
			tiers: []Tier{
				{Name: "a", Items: []Item{{Text: "hello", Priority: 1}}, MinItems: 0},
			},
			budget:      100,
			wantOut:     "hello",
			wantDropped: 0,
		},
		{
			// Pass 1 reserves 1 item from tier B even though tier A items don't fit either
			// (tier A items are too big).  Budget = 10; A item = 20 bytes (won't fit);
			// B item = 5 bytes (fits in reserve pass).
			name: "MinItems reserves lower tier item when upper tier items too large",
			tiers: []Tier{
				{Name: "A", Items: []Item{{Text: strings.Repeat("X", 20), Priority: 1}}, MinItems: 0},
				{Name: "B", Items: []Item{{Text: "small", Priority: 1}}, MinItems: 1},
			},
			budget:      10,
			wantOut:     "small",
			wantDropped: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotDropped := Assemble(tc.tiers, tc.budget)
			if got != tc.wantOut {
				t.Errorf("out = %q, want %q", got, tc.wantOut)
			}
			if gotDropped != tc.wantDropped {
				t.Errorf("dropped = %d, want %d", gotDropped, tc.wantDropped)
			}
			// Verify byte ceiling is respected.
			if tc.budget > 0 && len(got) > tc.budget {
				t.Errorf("output length %d exceeds budget %d", len(got), tc.budget)
			}
		})
	}
}

// TestAssembleDeterminism runs Assemble twice with the same input and verifies
// the output is identical.
func TestAssembleDeterminism(t *testing.T) {
	tiers := []Tier{
		{Name: "first", Items: []Item{
			{Text: "c", Priority: 3},
			{Text: "a", Priority: 1},
			{Text: "b", Priority: 2},
		}, MinItems: 2},
		{Name: "second", Items: []Item{
			{Text: "z", Priority: 10},
			{Text: "y", Priority: 5},
		}, MinItems: 1},
	}
	out1, d1 := Assemble(tiers, 50)
	out2, d2 := Assemble(tiers, 50)
	if out1 != out2 || d1 != d2 {
		t.Errorf("non-deterministic: run1=(%q,%d), run2=(%q,%d)", out1, d1, out2, d2)
	}
}
