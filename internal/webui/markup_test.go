package webui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// These check the page's structure rather than its looks, because structure is what
// assistive technology reads and what nothing else in this repository verifies.
//
// They exist because of three defects that all shipped and all survived a careful
// reading. The tab list declared role="tab" with no keyboard handling at all, which is
// worse than not using the pattern: it tells a screen reader the arrow keys work, and
// they did not. The steps were spans, so the page had one heading and could not be
// navigated by structure. And the buttons were styled by their HTML type, which held
// until a form had two of them and then put the measuring button above the acting one,
// at a different height, looking more important.
//
// None of that is visible in a screenshot of a working page, which is exactly why it
// needs a test rather than another look.

func asset(t *testing.T, name string) string {
	t.Helper()
	b, err := assets.ReadFile("assets/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// Every form control needs an accessible name. A field a screen reader announces as
// "edit text, blank" is a field nobody can fill in.
func TestEveryControlHasALabel(t *testing.T) {
	page := asset(t, "index.html")

	labelled := map[string]bool{}
	for _, m := range regexp.MustCompile(`<label[^>]*\bfor="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		labelled[m[1]] = true
	}

	// A control wrapped in a label needs no `for`, so those are excluded by id.
	wrapped := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?s)<label class="inline">(.*?)</label>`).FindAllStringSubmatch(page, -1) {
		for _, id := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
			wrapped[id[1]] = true
		}
	}

	controls := regexp.MustCompile(`<(input|select|textarea)\b[^>]*>`)
	for _, tag := range controls.FindAllString(page, -1) {
		id := attr(tag, "id")
		if id == "" {
			t.Errorf("a control has no id, so no label can point at it: %s", tag)
			continue
		}
		if labelled[id] || wrapped[id] ||
			strings.Contains(tag, "aria-label") || strings.Contains(tag, "aria-labelledby") {
			continue
		}
		t.Errorf("%s has no accessible name", id)
	}
}

// A tab that points at a panel which does not exist is a tab that does nothing, and the
// failure is silent in every browser.
func TestEveryTabPointsAtItsPanel(t *testing.T) {
	page := asset(t, "index.html")

	tabs := regexp.MustCompile(`<button[^>]*role="tab"[^>]*>`).FindAllString(page, -1)
	if len(tabs) < 2 {
		t.Fatalf("found %d tabs, which is not a tab list", len(tabs))
	}
	for _, tag := range tabs {
		panel := attr(tag, "aria-controls")
		if panel == "" {
			t.Errorf("a tab controls nothing: %s", tag)
			continue
		}
		if !strings.Contains(page, fmt.Sprintf(`id="%s"`, panel)) {
			t.Errorf("tab points at %q, which is not on the page", panel)
		}
		if !strings.Contains(page, fmt.Sprintf(`aria-labelledby="%s"`, attr(tag, "id"))) {
			t.Errorf("panel %q does not name its tab back, so it has no accessible name", panel)
		}
	}
}

// The one that would have caught the original defect. Declaring the tab role is a
// promise about the keyboard, and the promise has to be kept somewhere.
//
// Its reach is worth stating plainly, because a test that overstates itself is the thing
// this repository keeps finding: it catches the handler being absent, not the handler
// being wrong. Proving it red by prefixing "ArrowRight" to "XArrowRight" did not work,
// since a substring check accepts that, which is why the keys are matched as the string
// literals a handler would actually compare against. Whether the arrows land on the
// right tab is only answered by driving a browser, which the interface was, by hand.
func TestTabListIsOperableByKeyboard(t *testing.T) {
	script := asset(t, "app.js")

	if !strings.Contains(script, `addEventListener('keydown'`) {
		t.Fatal(`nothing listens for keydown; role="tab" claims a keyboard behaviour that is not implemented`)
	}
	// Quoted, so a mention in prose or a near-miss identifier does not satisfy them.
	for _, key := range []string{`'ArrowRight'`, `'ArrowLeft'`, `'Home'`, `'End'`} {
		if !strings.Contains(script, key) && !strings.Contains(script, strings.Trim(key, "'")+":") {
			t.Errorf("no handling for %s, which the tab pattern requires", key)
		}
	}
	// Without a roving tabindex the four tabs are four stops on the way to the form.
	if !strings.Contains(script, "tabIndex") {
		t.Error("no roving tabindex, so every tab sits in the page's tab order")
	}
}

// Steps are the page's structure. As spans they were invisible to it.
func TestStepsAreHeadings(t *testing.T) {
	page := asset(t, "index.html")
	if n := strings.Count(page, `<span class="step"`); n != 0 {
		t.Errorf("%d steps are still spans, so they carry no structure", n)
	}
	if n := strings.Count(page, `<h2 class="step"`); n < 4 {
		t.Errorf("only %d steps are headings; the page cannot be navigated by structure", n)
	}
}

// Buttons declare their role in the interface. Styling them by HTML type is what put a
// secondary action above a primary one, and it did it through a specificity accident
// rather than through anything anyone wrote down.
func TestButtonsDeclareTheirRole(t *testing.T) {
	page := asset(t, "index.html")
	css := asset(t, "app.css")

	if strings.Contains(css, `button[type="submit"] {`) || strings.Contains(css, `button[type="submit"],`) {
		t.Error("app.css still styles buttons by type; importance is not something an attribute knows")
	}
	if strings.Contains(page, `class="action"`) {
		t.Error(`the "action" class is gone; something still uses it`)
	}

	for _, tag := range regexp.MustCompile(`<button\b[^>]*>`).FindAllString(page, -1) {
		if strings.Contains(tag, `role="tab"`) {
			continue
		}
		class := attr(tag, "class")
		if !strings.Contains(class, "btn-primary") && !strings.Contains(class, "btn-secondary") {
			t.Errorf("a button states no importance, so it will inherit one by accident: %s", tag)
		}
	}
}

// Two regressions the layout actually had, both invisible at desktop width.
//
// The tab bar was four items across inside `overflow: hidden`, and flex items refuse to
// shrink below their own text: at 320 CSS pixels the row measured 316 px inside a 273 px
// box and the last tab was clipped away in silence. And the body copy was set in
// absolute pixels while everything around it was in rem, so it alone ignored both the
// browser's zoom and the reader's default font size.
//
// Neither is provable from a stylesheet alone, so these check the specific mistake
// rather than the property. The property was checked by driving a browser at 320, 360,
// 480, 544, 560, 768 and 1280, and again at 200 percent.
func TestLayoutSurvivesNarrowScreens(t *testing.T) {
	css := asset(t, "app.css")

	if !regexp.MustCompile(`nav\s*\{[^}]*flex-wrap:\s*wrap`).MatchString(css) {
		t.Error("the tab bar does not wrap, and it sits inside overflow:hidden, so a tab that does not fit disappears without a trace")
	}
	if !strings.Contains(css, "@media (max-width:") {
		t.Error("no width breakpoint at all; the header and the tab bar both commit to a fixed column count")
	}
	if regexp.MustCompile(`body\s*\{[^}]*font:\s*\d+px`).MatchString(css) {
		t.Error("the body font size is absolute, so it ignores the reader's own font size preference")
	}
}

// The page told you to save the private identity, called losing it unrecoverable, and
// then offered no way to save it: select-all-and-copy by hand was the only route. An
// instruction an interface does not support is not an instruction, it is a reproach.
//
// Checked here because the buttons are built in JavaScript, so nothing else in this
// repository would notice them going away.
func TestGeneratedKeysCanBeSaved(t *testing.T) {
	script := asset(t, "app.js")

	for _, needed := range []string{"copyButton", "saveButton", "clipboard.writeText"} {
		if !strings.Contains(script, needed) {
			t.Errorf("no %s: the page still tells you to save something it gives you no way to save", needed)
		}
	}

	// The file has to be byte for byte what `keygen -out` writes, or an identity saved
	// from the page will not work with -identity on the command line. Proved by hand
	// against the real binary; pinned here so the newline is not tidied away later.
	if !strings.Contains(script, `value + '\n'`) {
		t.Error("the saved file does not end with a newline, so it no longer matches what keygen -out writes")
	}
	if !strings.Contains(script, "noisecrypt-identity.key") {
		t.Error("the private identity has no filename of its own")
	}

	// A copy with no visible effect leaves you pressing the button again to be sure.
	if !strings.Contains(script, "confirmOn") {
		t.Error("copying and saving give no feedback")
	}
}

func attr(tag, name string) string {
	m := regexp.MustCompile(name + `="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return m[1]
}
