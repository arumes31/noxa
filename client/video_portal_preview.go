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

type VideoPortalPreview struct {
	Title       string `json:"title"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail"`
	LiveState   string `json:"live_state,omitempty"`
	Category    string `json:"category,omitempty"`
	StartsAt    string `json:"starts_at,omitempty"`
}

var portalWhatnotUUID = regexp.MustCompile(`^[A-Fa-f0-9]{8}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{12}$`)
var portalWhatnotSlug = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var portalWhatnotUser = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
var portalNumericID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var portalDailyID = regexp.MustCompile(`^[A-Za-z0-9]{1,20}$`)
var portalTwitchClip = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var portalTwitchChannel = regexp.MustCompile(`^[a-z0-9_]{1,25}$`)
var portalDailyCDN = regexp.MustCompile(`^s[0-9]?\.dmcdn\.net$`)
var portalTwitchClipCDN = regexp.MustCompile(`^clips-media-assets[0-9]?\.twitch\.tv$`)

func portalPreviewEndpoint(provider, resource string) (string, error) {
	switch provider {
	case "whatnot":
		if resource == "home" {
			return "https://www.whatnot.com/", nil
		}
		kind, id, found := strings.Cut(resource, ":")
		if found {
			valid := ((kind == "live" || kind == "clip" || kind == "listing") && portalWhatnotUUID.MatchString(id)) || (kind == "s" && portalWhatnotSlug.MatchString(id)) || (kind == "user" && portalWhatnotUser.MatchString(id) && id != "." && id != "..")
			if valid {
				return "https://www.whatnot.com/" + kind + "/" + id, nil
			}
		}
	case "vimeo":
		if portalNumericID.MatchString(resource) {
			return "https://vimeo.com/api/oembed.json?" + url.Values{"url": {"https://vimeo.com/" + resource}, "maxwidth": {"480"}}.Encode(), nil
		}
	case "dailymotion":
		if portalDailyID.MatchString(resource) {
			return "https://www.dailymotion.com/services/oembed?" + url.Values{"url": {"https://www.dailymotion.com/video/" + resource}, "format": {"json"}, "maxwidth": {"480"}}.Encode(), nil
		}
	case "twitch":
		kind, id, found := strings.Cut(resource, ":")
		if found {
			switch kind {
			case "clip":
				if portalTwitchClip.MatchString(id) {
					return "https://clips.twitch.tv/" + id, nil
				}
			case "video":
				if portalNumericID.MatchString(id) {
					return "https://www.twitch.tv/videos/" + id, nil
				}
			case "channel":
				if portalTwitchChannel.MatchString(id) {
					return "https://www.twitch.tv/" + id, nil
				}
			}
		}
	}
	return "", fmt.Errorf("invalid video portal resource")
}

func safePortalThumbnail(provider, value string) string {
	if len(value) > 2048 {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	switch provider {
	case "whatnot":
		allowed = host == "images.whatnot.com" || host == "assets.whatnot.com" || host == "www.whatnot.com"
	case "twitch":
		allowed = host == "static-cdn.jtvnw.net" || portalTwitchClipCDN.MatchString(host)
	case "vimeo":
		allowed = host == "i.vimeocdn.com" || host == "secure-b.vimeocdn.com"
	case "dailymotion":
		allowed = portalDailyCDN.MatchString(host)
	}
	if !allowed {
		return ""
	}
	return u.String()
}

func (a *App) VideoPortalPreviewForTab(tabID, provider, resource string) (VideoPortalPreview, error) {
	if _, err := a.requireTabCM(tabID); err != nil {
		return VideoPortalPreview{}, err
	}
	settings := a.GetSettings()
	if settings.LinkPreviews != nil && !*settings.LinkPreviews {
		return VideoPortalPreview{}, fmt.Errorf("link previews disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return portalPreviewRedirect(provider, req, via) }}
	return fetchVideoPortalPreview(ctx, provider, resource, client.Do)
}

func fetchVideoPortalPreview(ctx context.Context, provider, resource string, do func(*http.Request) (*http.Response, error)) (VideoPortalPreview, error) {
	endpoint, err := portalPreviewEndpoint(provider, resource)
	if err != nil {
		return VideoPortalPreview{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return VideoPortalPreview{}, err
	}
	req.Header.Set("User-Agent", "noXaLinkPreview/1.0")
	req.Header.Set("Accept", "application/json")
	page := provider == "twitch" || provider == "whatnot"
	if page {
		req.Header.Set("Accept", "text/html")
	}
	resp, err := do(req)
	if err != nil {
		return VideoPortalPreview{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return VideoPortalPreview{}, fmt.Errorf("video preview unavailable (%d)", resp.StatusCode)
	}
	var result VideoPortalPreview
	if page {
		result = publicPagePreview(io.LimitReader(resp.Body, 256*1024))
	} else {
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
		if readErr != nil {
			return VideoPortalPreview{}, readErr
		}
		if len(payload) > 64*1024 {
			return VideoPortalPreview{}, fmt.Errorf("video preview too large")
		}
		var data struct {
			Title       string `json:"title"`
			Author      string `json:"author_name"`
			Description string `json:"description"`
			Thumbnail   string `json:"thumbnail_url"`
		}
		if err := json.Unmarshal(payload, &data); err != nil {
			return VideoPortalPreview{}, err
		}
		result = VideoPortalPreview{Title: data.Title, Author: data.Author, Description: data.Description, Thumbnail: data.Thumbnail}
	}
	result.Title = limitPreviewText(result.Title, 200)
	if result.Author == "" && provider == "whatnot" && strings.HasPrefix(resource, "user:") {
		result.Author = strings.TrimPrefix(resource, "user:")
	}
	result.Author = limitPreviewText(result.Author, 120)
	result.Description = limitPreviewText(result.Description, 160)
	result.Thumbnail = safePortalThumbnail(provider, result.Thumbnail)
	result.Category = limitPreviewText(result.Category, 100)
	if provider != "twitch" && provider != "whatnot" {
		result.LiveState = ""
		result.Category = ""
		result.StartsAt = ""
	}
	if provider == "twitch" && !strings.HasPrefix(resource, "channel:") {
		result.LiveState = ""
		result.StartsAt = ""
	}
	if result.Title == "" || (provider == "twitch" && result.Title == "Twitch") || (provider == "whatnot" && strings.HasPrefix(result.Title, "· Whatnot")) {
		return VideoPortalPreview{}, fmt.Errorf("video preview metadata unavailable")
	}
	return result, nil
}

func publicPagePreview(reader io.Reader) VideoPortalPreview {
	tokens := html.NewTokenizer(reader)
	tokens.SetMaxBuf(64 * 1024)
	var result VideoPortalPreview
	now := time.Now()
	inHead := true
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			return result
		}
		token := tokens.Token()
		if kind == html.EndTagToken && token.Data == "head" {
			inHead = false
			continue
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		attrs := map[string]string{}
		for _, attr := range token.Attr {
			attrs[attr.Key] = attr.Val
		}
		if token.Data == "script" && strings.EqualFold(attrs["type"], "application/ld+json") {
			if tokens.Next() == html.TextToken {
				data := tokens.Text()
				if len(data) <= 32*1024 {
					var value any
					if json.Unmarshal(data, &value) == nil {
						applyPortalStructuredData(&result, value, now, 0)
					}
				}
			}
			continue
		}
		if !inHead || token.Data != "meta" {
			continue
		}
		key := attrs["property"]
		if key == "" {
			key = attrs["name"]
		}
		value := attrs["content"]
		switch key {
		case "og:title":
			result.Title = value
		case "og:description":
			result.Description = value
		case "og:image":
			result.Thumbnail = value
		case "og:profile:username":
			result.Author = value
		case "video:tag":
			if result.Category == "" {
				result.Category = value
			}
		}
	}
}

// Structured data is parsed as bounded JSON, never executed. A live-event flag
// alone describes the kind of event; dates must place it in the present.
func applyPortalStructuredData(result *VideoPortalPreview, value any, now time.Time, depth int) {
	if depth > 8 {
		return
	}
	if list, ok := value.([]any); ok {
		for _, entry := range list {
			applyPortalStructuredData(result, entry, now, depth+1)
		}
		return
	}
	data, ok := value.(map[string]any)
	if !ok {
		return
	}
	text := func(key string) string { value, _ := data[key].(string); return value }
	kind := text("@type")
	if kind == "VideoObject" || kind == "Event" || kind == "BroadcastEvent" {
		if title := text("name"); title != "" {
			result.Title = title
		}
		if result.Description == "" {
			result.Description = text("description")
		}
		if result.Category == "" {
			result.Category = text("genre")
		}
		if result.Thumbnail == "" {
			result.Thumbnail = text("thumbnailUrl")
		}
		for _, key := range []string{"author", "creator", "organizer"} {
			if author, ok := data[key].(map[string]any); ok {
				if name, ok := author["name"].(string); ok && result.Author == "" {
					result.Author = name
				}
			}
		}
		start, err := time.Parse(time.RFC3339, text("startDate"))
		end, endErr := time.Parse(time.RFC3339, text("endDate"))
		if err == nil {
			result.StartsAt = start.UTC().Format(time.RFC3339)
			if start.After(now) {
				result.LiveState = "scheduled"
			} else if endErr == nil && !end.After(now) {
				result.LiveState = "offline"
			} else if live, ok := data["isLiveBroadcast"].(bool); ok && live {
				result.LiveState = "live"
			}
		}
		if kind == "VideoObject" {
			applyPortalStructuredData(result, data["publication"], now, depth+1)
		}
	}
	applyPortalStructuredData(result, data["@graph"], now, depth+1)
}

// Only Whatnot share links may redirect, and only to another recognized public
// Whatnot path. Other providers retain the no-redirect policy.
func portalPreviewRedirect(provider string, req *http.Request, via []*http.Request) error {
	if provider != "whatnot" || len(via) >= 3 || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Port() != "" || (req.URL.Hostname() != "www.whatnot.com" && req.URL.Hostname() != "whatnot.com") {
		return http.ErrUseLastResponse
	}
	resource := "home"
	if req.URL.Path != "/" {
		parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
		if len(parts) != 2 {
			return http.ErrUseLastResponse
		}
		resource = parts[0] + ":" + parts[1]
	}
	if _, err := portalPreviewEndpoint("whatnot", resource); err != nil {
		return http.ErrUseLastResponse
	}
	req.URL.RawQuery = ""
	return nil
}
