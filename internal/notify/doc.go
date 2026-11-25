package notify

import (
	"fmt"
	"html"
	"strings"
)

// Doc is one message, rendered as Mattermost Markdown, plain text (email) or HTML (email). Building the
// content once keeps the three in step; every piece of text is escaped by the renderer, so names,
// versions and summary headlines can't inject Markdown, HTML or @-mentions.
type Doc struct {
	Title    string
	Intro    []Line
	Sections []Section
	Footer   []Line

	vulns     *VulnSummary // the digest's numbers, stored after a scheduled send
	vulnsBase string       // what they were compared with
}

type Section struct {
	Heading string // empty = no heading (single-tenant digest)
	Blocks  []Block
}

// Block is a titled list. More > 0 adds "…and N more" (linking to MoreLink when set).
type Block struct {
	Title    string
	Empty    string // shown instead of the list when it has no items ("" = block omitted)
	Items    []Line
	More     int
	MoreLink string
}

// Line is a list item or paragraph made of spans.
type Line []Span

type Span struct {
	Text string
	Bold bool
	Code bool
	Link string // absolute URL built by Owl (never from upstream or Claude)
	Dim  bool
}

func T(s string) Span                  { return Span{Text: s} }
func B(s string) Span                  { return Span{Text: s, Bold: true} }
func C(s string) Span                  { return Span{Text: s, Code: true} }
func L(text, link string) Span         { return Span{Text: text, Link: link} }
func D(s string) Span                  { return Span{Text: s, Dim: true} }
func Ln(spans ...Span) Line            { return Line(spans) }
func (b Block) visible() bool          { return len(b.Items) > 0 || b.Empty != "" }
func (d Doc) Markdown() string         { return d.render(mdFormat) }
func (d Doc) Text() string             { return d.render(textFormat) }
func capLines(n, limit int) (int, int) { return min(n, limit), max(n-limit, 0) }

type format struct {
	span           func(Span) string
	title, heading func(string) string
	blockTitle     func(string) string
	item           string
	paraSep        string
}

var mdFormat = format{
	span:       mdSpan,
	title:      func(s string) string { return "#### " + mdEscape(s) },
	heading:    func(s string) string { return "##### " + mdEscape(s) },
	blockTitle: func(s string) string { return "**" + mdEscape(s) + "**" },
	item:       "- ",
	paraSep:    "\n",
}

var textFormat = format{
	span:       plainSpan,
	title:      func(s string) string { return s + "\n" + strings.Repeat("=", min(len([]rune(s)), 72)) },
	heading:    func(s string) string { return s + "\n" + strings.Repeat("-", min(len([]rune(s)), 72)) },
	blockTitle: func(s string) string { return s + ":" },
	item:       "  - ",
	paraSep:    "\n",
}

func (d Doc) render(f format) string {
	var b strings.Builder
	if d.Title != "" {
		b.WriteString(f.title(d.Title) + "\n")
	}
	for _, l := range d.Intro {
		b.WriteString(renderLine(l, f.span) + "\n")
	}
	for _, s := range d.Sections {
		if s.Heading != "" {
			b.WriteString("\n" + f.heading(s.Heading) + "\n")
		}
		for _, bl := range s.Blocks {
			if !bl.visible() {
				continue
			}
			b.WriteString("\n" + f.blockTitle(bl.Title) + "\n")
			if len(bl.Items) == 0 {
				b.WriteString(f.span(D(bl.Empty)) + "\n")
			}
			for _, it := range bl.Items {
				b.WriteString(f.item + renderLine(it, f.span) + "\n")
			}
			if bl.More > 0 {
				b.WriteString(f.item + renderLine(moreLine(bl), f.span) + "\n")
			}
		}
	}
	if len(d.Footer) > 0 {
		b.WriteString("\n")
		for _, l := range d.Footer {
			b.WriteString(renderLine(l, f.span) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func moreLine(bl Block) Line {
	t := fmt.Sprintf("…and %d more", bl.More)
	if bl.MoreLink != "" {
		return Ln(L(t, bl.MoreLink))
	}
	return Ln(D(t))
}

func renderLine(l Line, f func(Span) string) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(f(s))
	}
	return b.String()
}

func plainSpan(s Span) string {
	if s.Link != "" {
		return s.Text + " (" + s.Link + ")"
	}
	return s.Text
}

// mdEscape backslash-escapes Markdown punctuation and breaks @-mentions (@channel, @all, @user) with a
// zero-width space, so nothing in a message can format itself or notify people.
func mdEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		// ( ) ! need no escape: with [ ] escaped no link or image can form. # is escaped because Mattermost
		// turns "#word" into a hashtag search.
		case '\\', '`', '*', '_', '[', ']', '#', '|', '<', '>', '~':
			b.WriteRune('\\')
			b.WriteRune(r)
		case '@':
			b.WriteString("@​")
		case '\n', '\r':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func mdSpan(s Span) string {
	switch {
	case s.Link != "":
		return "[" + mdEscape(s.Text) + "](" + strings.NewReplacer("(", "%28", ")", "%29", " ", "%20").Replace(s.Link) + ")"
	case s.Code:
		// Backticks can't be escaped inside a code span; image names never contain them, but be safe.
		return "`" + strings.NewReplacer("`", "'", "\n", " ", "@", "@​").Replace(s.Text) + "`"
	case s.Bold:
		return "**" + mdEscape(s.Text) + "**"
	}
	return mdEscape(s.Text)
}

// HTML renders a self-contained email body: inline styles only, no images, no remote content.
func (d Doc) HTML() string {
	esc := html.EscapeString
	span := func(s Span) string {
		t := esc(s.Text)
		switch {
		case s.Link != "":
			return `<a href="` + esc(s.Link) + `" style="color:#574CF9">` + t + `</a>`
		case s.Code:
			return `<code style="font-family:ui-monospace,Menlo,monospace;font-size:13px">` + t + `</code>`
		case s.Bold:
			return "<b>" + t + "</b>"
		case s.Dim:
			return `<span style="color:#6b6b80">` + t + `</span>`
		}
		return t
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body style="margin:0;padding:24px;background:#f6f6fa;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.5;color:#1c1c28">`)
	b.WriteString(`<div style="max-width:640px;margin:0 auto;background:#ffffff;border-radius:8px;padding:24px">`)
	if d.Title != "" {
		b.WriteString(`<h1 style="font-size:20px;margin:0 0 12px">` + esc(d.Title) + `</h1>`)
	}
	for _, l := range d.Intro {
		b.WriteString(`<p style="margin:0 0 8px">` + renderLine(l, span) + `</p>`)
	}
	for _, s := range d.Sections {
		if s.Heading != "" {
			b.WriteString(`<h2 style="font-size:17px;margin:24px 0 4px;border-bottom:1px solid #e4e4ee;padding-bottom:4px">` + esc(s.Heading) + `</h2>`)
		}
		for _, bl := range s.Blocks {
			if !bl.visible() {
				continue
			}
			b.WriteString(`<h3 style="font-size:15px;margin:16px 0 4px">` + esc(bl.Title) + `</h3>`)
			if len(bl.Items) == 0 {
				b.WriteString(`<p style="margin:0">` + span(D(bl.Empty)) + `</p>`)
				continue
			}
			b.WriteString(`<ul style="margin:0;padding-left:20px">`)
			for _, it := range bl.Items {
				b.WriteString(`<li style="margin:2px 0">` + renderLine(it, span) + `</li>`)
			}
			if bl.More > 0 {
				b.WriteString(`<li style="margin:2px 0">` + renderLine(moreLine(bl), span) + `</li>`)
			}
			b.WriteString(`</ul>`)
		}
	}
	for _, l := range d.Footer {
		b.WriteString(`<p style="margin:16px 0 0;color:#6b6b80">` + renderLine(l, span) + `</p>`)
	}
	b.WriteString(`</div></body></html>`)
	return b.String()
}

// Fit cuts a Markdown message at a line boundary to fit Mattermost's post limit.
func Fit(md, more string) string {
	if len(md) <= maxPost {
		return md
	}
	cut := strings.LastIndexByte(md[:maxPost-len(more)-2], '\n')
	if cut < 0 {
		cut = maxPost - len(more) - 2
	}
	return md[:cut] + "\n" + more
}
