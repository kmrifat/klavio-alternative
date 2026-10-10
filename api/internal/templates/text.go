package templates

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// PlainText makes the text/plain part from compiled HTML. Every email is
// sent with both: some readers prefer it, and an HTML-only email scores
// worse with spam filters.
func PlainText(htmlBody string) string {
	doc, err := parseHTML(htmlBody)
	if err != nil {
		return ""
	}
	var b bytes.Buffer
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.CommentNode:
			return
		case html.TextNode:
			// Source line breaks are just spaces in HTML.
			b.WriteString(strings.ReplaceAll(n.Data, "\n", " "))
			return
		case html.ElementNode:
			if hidden(n) {
				return
			}
			switch n.Data {
			case "head", "style", "script", "title":
				return
			case "br":
				b.WriteString("\n")
				return
			case "img":
				if alt, _ := attr(n, "alt"); strings.TrimSpace(alt) != "" && !inside(n, "a") {
					b.WriteString(" " + alt + " ")
				}
				return
			case "a":
				link(&b, n, walk)
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && block[n.Data] {
			b.WriteString("\n")
		}
	}
	walk(doc)
	return tidyLines(b.String())
}

// link writes "label (url)", using an image's alt text when the link is an image.
func link(b *bytes.Buffer, n *html.Node, walk func(*html.Node)) {
	start := b.Len()
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	label := collapse(b.String()[start:])
	if label == "" {
		label = imgAlt(n)
	}
	b.Truncate(start)
	b.WriteString(label)
	href, _ := attr(n, "href")
	href = strings.TrimSpace(href)
	if href != "" && href != "#" && href != label && !strings.HasPrefix(href, "mailto:") {
		b.WriteString(" (" + href + ")")
	}
}

// hidden: MJML's preview text is a display:none div; it is not part of the body.
func hidden(n *html.Node) bool {
	style, _ := attr(n, "style")
	return strings.Contains(strings.ReplaceAll(strings.ToLower(style), " ", ""), "display:none")
}

var block = map[string]bool{
	"p": true, "div": true, "tr": true, "table": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "li": true, "ul": true, "ol": true, "section": true,
	"blockquote": true, "hr": true,
}

func imgAlt(n *html.Node) string {
	var alt string
	var find func(*html.Node)
	find = func(n *html.Node) {
		if alt != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "img" {
			alt, _ = attr(n, "alt")
			alt = strings.TrimSpace(alt)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(n)
	return alt
}

// tidyLines collapses spaces within lines and keeps at most one blank line.
func tidyLines(s string) string {
	var out []string
	blank := true // drop leading blank lines
	for _, line := range strings.Split(s, "\n") {
		line = collapse(line)
		if line == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n")) + "\n"
}
