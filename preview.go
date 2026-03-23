package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	ogTitleRe  = regexp.MustCompile(`(?i)<meta\s+(?:[^>]*?\s+)?(?:property|name)=["']og:title["']\s+content=["']([^"']+)["']`)
	ogDescRe   = regexp.MustCompile(`(?i)<meta\s+(?:[^>]*?\s+)?(?:property|name)=["']og:description["']\s+content=["']([^"']+)["']`)
	ogImageRe  = regexp.MustCompile(`(?i)<meta\s+(?:[^>]*?\s+)?(?:property|name)=["']og:image["']\s+content=["']([^"']+)["']`)
	ogSiteRe   = regexp.MustCompile(`(?i)<meta\s+(?:[^>]*?\s+)?(?:property|name)=["']og:site_name["']\s+content=["']([^"']+)["']`)
	titleRe    = regexp.MustCompile(`(?i)<title[^>]*>([^<]+)</title>`)
	urlRe      = regexp.MustCompile(`https?://[^\s<>"]+`)
	ogTitleRe2 = regexp.MustCompile(`(?i)<meta\s+content=["']([^"']+)["']\s+(?:property|name)=["']og:title["']`)
	ogDescRe2  = regexp.MustCompile(`(?i)<meta\s+content=["']([^"']+)["']\s+(?:property|name)=["']og:description["']`)
	ogImageRe2 = regexp.MustCompile(`(?i)<meta\s+content=["']([^"']+)["']\s+(?:property|name)=["']og:image["']`)
	ogSiteRe2  = regexp.MustCompile(`(?i)<meta\s+content=["']([^"']+)["']\s+(?:property|name)=["']og:site_name["']`)
)

var previewClient = &http.Client{
	Timeout: 4 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

func matchOG(html string, re1, re2 *regexp.Regexp) string {
	if m := re1.FindStringSubmatch(html); len(m) > 1 {
		return m[1]
	}
	if m := re2.FindStringSubmatch(html); len(m) > 1 {
		return m[1]
	}
	return ""
}

func fetchPreview(rawURL string) (*LinkPreview, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid url")
	}
	// SSRF protection: block private IPs
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return nil, fmt.Errorf("blocked")
		}
	}

	req, _ := http.NewRequest("GET", rawURL, nil)
	req.Header.Set("User-Agent", "feed-bot/1.0 (link preview)")
	req.Header.Set("Accept", "text/html")

	resp, err := previewClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	html := string(body)

	title := matchOG(html, ogTitleRe, ogTitleRe2)
	if title == "" {
		if m := titleRe.FindStringSubmatch(html); len(m) > 1 {
			title = strings.TrimSpace(m[1])
		}
	}
	if title == "" {
		return nil, fmt.Errorf("no title found")
	}

	return &LinkPreview{
		URL:         rawURL,
		Title:       title,
		Description: matchOG(html, ogDescRe, ogDescRe2),
		Image:       matchOG(html, ogImageRe, ogImageRe2),
		SiteName:    matchOG(html, ogSiteRe, ogSiteRe2),
	}, nil
}

func extractFirstURL(text string) string {
	m := urlRe.FindString(text)
	return strings.TrimRight(m, ".,;:!?)")
}
