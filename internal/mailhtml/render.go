package mailhtml

import (
	"bytes"
	"html/template"
	"strings"
)

// The style constants are put into the template text once, when it is parsed.
// They are fixed strings of ours, never data.
const (
	fontStack = `-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif`
	monoStack = `ui-monospace,SFMono-Regular,Menlo,Consolas,'Liberation Mono',monospace`
)

var page = template.Must(template.New("doc").Parse(strings.NewReplacer(
	"@@FONT@@", fontStack,
	"@@MONO@@", monoStack,
).Replace(pageSource)))

// Render draws the document as an HTML page. If the template ever fails it
// returns an empty string, and the mail is then sent as plain text only.
func Render(d Doc) string {
	d = normalize(d)
	var buf bytes.Buffer
	if err := page.ExecuteTemplate(&buf, "doc", d); err != nil {
		return ""
	}
	return buf.String()
}

// normalize copies the right-alignment of each column into its cells, because
// the template cannot walk two lists side by side. It leaves the input alone.
func normalize(d Doc) Doc {
	out := d
	out.Sections = make([]Section, len(d.Sections))
	for i, s := range d.Sections {
		blocks := make([]Block, len(s.Blocks))
		for j, b := range s.Blocks {
			if b.Table != nil {
				t := Table{Cols: b.Table.Cols, Rows: make([][]Cell, len(b.Table.Rows))}
				for r, row := range b.Table.Rows {
					cells := append([]Cell(nil), row...)
					for c := range cells {
						if c < len(t.Cols) {
							cells[c].Right = t.Cols[c].Right
						}
					}
					t.Rows[r] = cells
				}
				b.Table = &t
			}
			blocks[j] = b
		}
		out.Sections[i] = Section{Heading: s.Heading, Blocks: blocks}
	}
	return out
}

const pageSource = `{{define "doc"}}<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light"><meta name="supported-color-schemes" content="light"><title>{{.Title}}</title></head>
<body bgcolor="#F3F4F6" style="margin:0;padding:0;background-color:#F3F4F6;color:#1F2937;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#F3F4F6" style="background-color:#F3F4F6;"><tr><td align="center" bgcolor="#F3F4F6" style="padding:16px 8px;background-color:#F3F4F6;">
<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:600px;">
<tr><td bgcolor="#7C3AED" style="background-color:#7C3AED;padding:14px 20px;border-radius:8px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>
<td bgcolor="#7C3AED" style="font-family:@@FONT@@;font-size:18px;font-weight:bold;color:#FFFFFF;background-color:#7C3AED;">EternalShell</td>
<td align="right" bgcolor="#7C3AED" style="font-family:@@FONT@@;font-size:14px;color:#FFFFFF;background-color:#7C3AED;">{{.Server}}</td>
</tr></table></td></tr>
<tr><td height="8" bgcolor="#F3F4F6" style="height:8px;font-size:1px;line-height:8px;background-color:#F3F4F6;">&nbsp;</td></tr>
<tr><td>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#FFFFFF" style="background-color:#FFFFFF;border:1px solid #E5E7EB;border-radius:8px;"><tr><td bgcolor="#FFFFFF" style="padding:18px 20px;background-color:#FFFFFF;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr><td bgcolor="{{.Badge.Level.Color}}" style="background-color:{{.Badge.Level.Color}};color:#FFFFFF;padding:5px 12px;border-radius:12px;font-family:@@FONT@@;font-size:13px;font-weight:bold;">{{.Badge.Text}}</td></tr></table>
<div style="margin:12px 0 0;font-family:@@FONT@@;font-size:20px;font-weight:bold;line-height:1.3;color:#111827;background-color:#FFFFFF;">{{.Title}}</div>
{{if .Lead}}<div style="margin:6px 0 0;font-family:@@FONT@@;font-size:13px;line-height:1.5;color:#6B7280;background-color:#FFFFFF;">{{.Lead}}</div>{{end}}
</td></tr></table></td></tr>
{{range .Sections}}<tr><td height="8" bgcolor="#F3F4F6" style="height:8px;font-size:1px;line-height:8px;background-color:#F3F4F6;">&nbsp;</td></tr>
<tr><td>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#FFFFFF" style="background-color:#FFFFFF;border:1px solid #E5E7EB;border-radius:8px;"><tr><td bgcolor="#FFFFFF" style="padding:14px 20px;background-color:#FFFFFF;">
<div style="margin:0 0 10px;font-family:@@FONT@@;font-size:12px;font-weight:bold;letter-spacing:0.06em;text-transform:uppercase;color:#7C3AED;background-color:#FFFFFF;">{{.Heading}}</div>
{{range .Blocks}}{{template "block" .}}{{end}}
</td></tr></table></td></tr>
{{end}}<tr><td align="center" bgcolor="#F3F4F6" style="padding:16px 8px 0;font-family:@@FONT@@;font-size:12px;line-height:1.5;color:#6B7280;background-color:#F3F4F6;">{{.Footer}}</td></tr>
</table></td></tr></table>
</body></html>{{end}}

{{define "block"}}{{if eq .Kind "text"}}<div style="margin:0 0 8px;font-family:@@FONT@@;font-size:14px;line-height:1.5;color:#1F2937;background-color:#FFFFFF;">{{.Text}}</div>
{{else if eq .Kind "note"}}<div style="margin:4px 0 0;font-family:@@FONT@@;font-size:12px;line-height:1.5;color:#6B7280;background-color:#FFFFFF;">{{.Text}}</div>
{{else if eq .Kind "list"}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#FFFFFF" style="background-color:#FFFFFF;">{{range .Items}}<tr><td width="16" valign="top" bgcolor="#FFFFFF" style="padding:3px 0;font-family:@@FONT@@;font-size:14px;line-height:1.5;color:{{.Level.Color}};background-color:#FFFFFF;">&#9679;</td><td valign="top" bgcolor="#FFFFFF" style="padding:3px 0;font-family:@@FONT@@;font-size:14px;line-height:1.5;color:#1F2937;background-color:#FFFFFF;word-break:break-word;">{{.Text}}</td></tr>{{end}}</table>
{{else if eq .Kind "facts"}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#FFFFFF" style="background-color:#FFFFFF;">{{range .Facts}}<tr><td width="34%" valign="top" bgcolor="#FFFFFF" style="padding:3px 10px 3px 0;font-family:@@FONT@@;font-size:13px;line-height:1.5;color:#6B7280;background-color:#FFFFFF;">{{.Label}}</td><td valign="top" bgcolor="#FFFFFF" style="padding:3px 0;font-family:{{if .Mono}}@@MONO@@{{else}}@@FONT@@{{end}};font-size:{{if .Mono}}13{{else}}14{{end}}px;line-height:1.5;color:{{.Tone.Color}};background-color:#FFFFFF;word-break:break-word;">{{.Value}}</td></tr>{{end}}</table>
{{else if eq .Kind "table"}}{{template "table" .Table}}
{{else if eq .Kind "meters"}}{{range .Meters}}{{template "meter" .}}{{end}}
{{end}}{{end}}

{{define "table"}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#FFFFFF" style="background-color:#FFFFFF;">
<tr>{{range .Cols}}<td align="{{if .Right}}right{{else}}left{{end}}" bgcolor="#FFFFFF" style="padding:4px 6px 4px 0;font-family:@@FONT@@;font-size:11px;font-weight:bold;color:#6B7280;background-color:#FFFFFF;border-bottom:1px solid #E5E7EB;">{{.Title}}</td>{{end}}</tr>
{{range .Rows}}<tr>{{range .}}<td align="{{if .Right}}right{{else}}left{{end}}" valign="top" bgcolor="#FFFFFF" style="padding:5px 6px 5px 0;font-family:{{if .Mono}}@@MONO@@{{else}}@@FONT@@{{end}};font-size:{{if .Mono}}13{{else}}14{{end}}px;line-height:1.4;color:{{.Tone.Color}};background-color:#FFFFFF;border-bottom:1px solid #F3F4F6;word-break:break-word;">{{.Text}}{{if .Sub}}<div style="font-family:@@FONT@@;font-size:12px;line-height:1.4;color:#6B7280;background-color:#FFFFFF;">{{.Sub}}</div>{{end}}</td>{{end}}</tr>
{{end}}</table>{{end}}

{{define "meter"}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#FFFFFF" style="background-color:#FFFFFF;margin:0 0 10px;">
<tr><td bgcolor="#FFFFFF" style="font-family:@@MONO@@;font-size:13px;color:#1F2937;background-color:#FFFFFF;">{{.Label}}</td><td align="right" bgcolor="#FFFFFF" style="font-family:@@MONO@@;font-size:13px;font-weight:bold;color:#1F2937;background-color:#FFFFFF;">{{.PctText}}</td></tr>
<tr><td colspan="2" bgcolor="#FFFFFF" style="padding:4px 0;background-color:#FFFFFF;"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#E5E7EB" style="background-color:#E5E7EB;border-radius:4px;"><tr><td width="{{.Pct}}%" height="8" bgcolor="{{.Fill.Color}}" style="width:{{.Pct}}%;height:8px;font-size:1px;line-height:8px;background-color:{{.Fill.Color}};border-radius:4px;">&nbsp;</td><td bgcolor="#E5E7EB" style="font-size:1px;line-height:8px;background-color:#E5E7EB;">&nbsp;</td></tr></table></td></tr>
<tr><td bgcolor="#FFFFFF" style="font-family:@@MONO@@;font-size:12px;color:#6B7280;background-color:#FFFFFF;">{{.Detail}}</td><td align="right" bgcolor="#FFFFFF" style="font-family:@@MONO@@;font-size:12px;color:{{.ChangeTone.Color}};background-color:#FFFFFF;">{{.Change}}</td></tr>
</table>{{end}}`
