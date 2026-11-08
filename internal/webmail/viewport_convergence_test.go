package webmail

import (
	"strings"
	"testing"
)

// TestAdaptHTMLKeepsNestedContainersUnforced pins the blunt-tool convergence:
// only top-level containers and tables are forced fluid. Nested divs without
// an explicit width (flex and inline-block rows in modern mail) keep their
// markup, so horizontal element rows stop stacking into one vertical column.
func TestAdaptHTMLKeepsNestedContainersUnforced(t *testing.T) {
	in := `<div style="padding:4px"><div style="display:flex"><div style="display:inline-block">a</div><div style="display:inline-block">b</div></div></div>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	// Nested flex and inline-block wrappers gain no fluid style: the
	// appended suffix would read display:<x>;max-width:100%;width:100%.
	for _, forced := range []string{"display:flex;max-width", "display:inline-block;max-width"} {
		if strings.Contains(out, forced) {
			t.Fatalf("nested container was forced full-width (%s): %s", forced, out)
		}
	}
	// The top-level container is still forced fluid so the layout scales.
	if !strings.Contains(out, "padding:4px;max-width:100%;width:100%") {
		t.Fatalf("top-level container lost the fluid style: %s", out)
	}

	// Tables stay fluid at any depth: they are the mail layout skeleton.
	nestedTable := AdaptHTMLForViewport(`<div><table><tr><td>x</td></tr></table></div>`, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(nestedTable, "max-width:100%;width:100%") {
		t.Fatalf("nested table lost the fluid style: %s", nestedTable)
	}
}

// TestAdaptHTMLRestylesFixedMaxAndMinWidth covers the widthStyleRE extension
// to the constraint declarations: oversized max-width caps become 100% in
// every bucket, oversized min-width floors are dropped on narrow panes only,
// and small constraints keep the design untouched.
func TestAdaptHTMLRestylesFixedMaxAndMinWidth(t *testing.T) {
	in := `<div style="max-width:600px;min-width:550px;width:600px"><p>x</p></div>` +
		`<div style="max-width:200px;min-width:100px"><p>small</p></div>`

	narrow := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if strings.Contains(narrow, "max-width:600px") {
		t.Fatalf("oversized max-width cap kept: %s", narrow)
	}
	if strings.Contains(narrow, "min-width:550px") {
		t.Fatalf("oversized min-width floor kept on narrow: %s", narrow)
	}
	if !strings.Contains(narrow, "width:100%") {
		t.Fatalf("oversized width not fluidized: %s", narrow)
	}
	if !strings.Contains(narrow, "max-width:200px") || !strings.Contains(narrow, "min-width:100px") {
		t.Fatalf("small constraints were rewritten: %s", narrow)
	}

	wide := AdaptHTMLForViewport(in, Viewport{UAClass: "desktop", Width: 1280})
	if strings.Contains(wide, "max-width:600px") {
		t.Fatalf("oversized max-width cap kept on desktop: %s", wide)
	}
	if !strings.Contains(wide, "min-width:550px") {
		t.Fatalf("desktop lost the design min-width floor: %s", wide)
	}
	// Explicit width declarations are fluidized in every bucket (long
	// standing behavior); only the narrow pane drops the min-width floor.
	if strings.Contains(wide, "width:600px") {
		t.Fatalf("desktop kept the oversized width: %s", wide)
	}
}

// TestAdaptHTMLLeavesCalcWidthsAlone locks the calc() contract: a width
// declared as calc(100% - Npx) is already fluid, so the fixed-width regex
// must not touch it and the fluid append must not shadow it with a later
// width:100% in the same declaration list.
func TestAdaptHTMLLeavesCalcWidthsAlone(t *testing.T) {
	in := `<div style="width:calc(100% - 480px)"><p>x</p></div>`
	out := AdaptHTMLForViewport(in, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(out, "width:calc(100% - 480px)") {
		t.Fatalf("calc width was rewritten: %s", out)
	}
	if strings.Contains(out, "calc(100% - 480px);max-width:100%;width:100%") {
		t.Fatalf("fluid append shadowed the calc width: %s", out)
	}

	// A nested calc-width sidebar keeps its declaration completely: no
	// forced fluid style may land on it at all.
	nested := AdaptHTMLForViewport(`<div><div style="width:calc(100% - 200px)">side</div></div>`, Viewport{UAClass: "mobile", Width: 375})
	if !strings.Contains(nested, `style="width:calc(100% - 200px)"`) {
		t.Fatalf("nested calc sidebar was rewritten: %s", nested)
	}
}
