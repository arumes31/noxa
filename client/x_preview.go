package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type XPostPreview struct {
	Author string `json:"author"`
	Text   string `json:"text"`
}

var xPostID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// Accept only a post ID. The provider endpoint is fixed and no scripts,
// remote images or provider HTML cross the native-to-renderer boundary.
func (a *App) XPostPreviewForTab(tabID, postID string) (XPostPreview, error) {
	if _, err := a.requireTabCM(tabID); err != nil {
		return XPostPreview{}, err
	}
	settings := a.GetSettings()
	if settings.LinkPreviews != nil && !*settings.LinkPreviews {
		return XPostPreview{}, fmt.Errorf("link previews disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return fetchXPostPreview(ctx, postID, client.Do)
}

func fetchXPostPreview(ctx context.Context, postID string, do func(*http.Request) (*http.Response, error)) (XPostPreview, error) {
	if !xPostID.MatchString(postID) {
		return XPostPreview{}, fmt.Errorf("invalid X post ID")
	}
	query := url.Values{"url": {"https://x.com/i/status/" + postID}, "omit_script": {"true"}, "dnt": {"true"}, "hide_thread": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://publish.x.com/oembed?"+query.Encode(), nil)
	if err != nil {
		return XPostPreview{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := do(req)
	if err != nil {
		return XPostPreview{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return XPostPreview{}, fmt.Errorf("x post preview unavailable (%d)", resp.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil {
		return XPostPreview{}, err
	}
	if len(payload) > 64*1024 {
		return XPostPreview{}, fmt.Errorf("x post preview too large")
	}
	var data struct {
		Author string `json:"author_name"`
		HTML   string `json:"html"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		return XPostPreview{}, err
	}
	text := xPostText(data.HTML)
	if text == "" {
		return XPostPreview{}, fmt.Errorf("x post text unavailable")
	}
	return XPostPreview{Author: limitPreviewText(data.Author, 120), Text: text}, nil
}

func xPostText(fragment string) string {
	doc, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		return ""
	}
	var paragraph func(*html.Node) *html.Node
	paragraph = func(node *html.Node) *html.Node {
		if node.Type == html.ElementNode && node.Data == "p" {
			return node
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if found := paragraph(child); found != nil {
				return found
			}
		}
		return nil
	}
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
		}
		if node.Type == html.ElementNode {
			switch node.Data {
			case "script", "style", "iframe", "object":
				return
			case "br":
				text.WriteByte('\n')
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	if post := paragraph(doc); post != nil {
		walk(post)
	}
	return limitPreviewText(text.String(), 2000)
}
