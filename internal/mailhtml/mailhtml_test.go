package mailhtml

import (
	"strings"
	"testing"
	"time"
)

// One sample document that uses every kind of block and a hostile value; it has
// to render, escape the value, and keep to the rules for mail HTML.
func TestRenderSample(t *testing.T) {
	const evil = `<script>alert(1)</script>" onmouseover="x`

	d := New("main-vps", time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC))
	d.Badge = Badge{Level: LevelWarn, Text: "2 things need attention"}
	d.Title = "main-vps status report"
	d.Lead = "Since the previous report."
	d.Sections = []Section{
		{Heading: "Needs attention", Blocks: []Block{
			List(Item{Text: "container shop/db: exited (code 1)", Level: LevelWarn}, Item{Text: evil, Level: LevelBad}),
			Text("A paragraph."), Note("A small note."),
		}},
		{Heading: "Disk", Blocks: []Block{
			Meters(
				Meter{Label: "/", Detail: "93.0G of 100.0G", Percent: 93, Fill: MeterFill(93, 90), Change: "+3.0G", ChangeTone: ToneUp},
				Meter{Label: "/data", Detail: "5.0G of 50.0G", Percent: 10, Fill: MeterFill(10, 90), Change: "-1.0G", ChangeTone: ToneDown},
			),
		}},
		{Heading: "Projects", Blocks: []Block{
			TableOf([]Col{{Title: "Project"}, {Title: "Size", Right: true}},
				[][]Cell{{{Text: evil, Sub: "layers 3.0G"}, {Text: "31.0G", Mono: true, Tone: ToneUp}}}),
		}},
		{Heading: "Uptime", Blocks: []Block{Facts(Fact{Label: "Up", Value: "3 d 5 h", Mono: true})}},
	}

	out := Render(d)
	if out == "" {
		t.Fatal("the template failed to render")
	}

	for _, want := range []string{
		"EternalShell", "main-vps", "2 things need attention", "Needs attention",
		"&lt;script&gt;alert(1)&lt;/script&gt;", // escaped
		"Sent by eternalshell on", "2026-10-25 08:00 UTC",
		"#7C3AED", "#DC2626", // the purple header/bars and a red bar
		"background-color:#FFFFFF", "color:#1F2937", // text and an explicit background
		`name="color-scheme"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output should contain %q", want)
		}
	}
	for _, bad := range []string{"<script", `onmouseover="x`, "http://", "https://", "<img", "<style", "<link", "src="} {
		if strings.Contains(out, bad) {
			t.Errorf("output must not contain %q", bad)
		}
	}
}
