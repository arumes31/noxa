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
)

type YouTubePreview struct {
	Title  string `json:"title"`
	Author string `json:"author"`
}

var youtubeVideoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// Only a video ID crosses this boundary. The endpoint is fixed, redirects
// are refused, and provider HTML and remote thumbnail URLs are never used.
func (a *App) YouTubePreviewForTab(tabID, videoID string) (YouTubePreview, error) {
	if _, err := a.requireTabCM(tabID); err != nil {
		return YouTubePreview{}, err
	}
	settings := a.GetSettings()
	if settings.LinkPreviews != nil && !*settings.LinkPreviews {
		return YouTubePreview{}, fmt.Errorf("link previews disabled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return fetchYouTubePreview(ctx, videoID, client.Do)
}

func fetchYouTubePreview(ctx context.Context, videoID string, do func(*http.Request) (*http.Response, error)) (YouTubePreview, error) {
	if !youtubeVideoID.MatchString(videoID) {
		return YouTubePreview{}, fmt.Errorf("invalid YouTube video ID")
	}
	query := url.Values{"url": {"https://www.youtube.com/watch?v=" + videoID}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.youtube.com/oembed?"+query.Encode(), nil)
	if err != nil {
		return YouTubePreview{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := do(req)
	if err != nil {
		return YouTubePreview{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return YouTubePreview{}, fmt.Errorf("YouTube preview unavailable (%d)", resp.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil {
		return YouTubePreview{}, err
	}
	if len(payload) > 64*1024 {
		return YouTubePreview{}, fmt.Errorf("YouTube preview too large")
	}
	var data struct {
		Title  string `json:"title"`
		Author string `json:"author_name"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		return YouTubePreview{}, err
	}
	return YouTubePreview{Title: limitPreviewText(data.Title, 200), Author: limitPreviewText(data.Author, 120)}, nil
}

func limitPreviewText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
