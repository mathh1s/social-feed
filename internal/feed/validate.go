package feed

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxAuthorLen   = 40
	maxContentLen  = 500
	maxAvatarBytes = 150_000
	maxImageBytes  = 700_000
	maxBodyBytes   = 1_000_000
	defaultLimit   = 20
	maxLimit       = 50
)

var imageDataURIRe = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,`)

func sanitize(s string) string { return strings.TrimSpace(s) }

func validateDataURI(raw string, maxBytes int, label string) (string, string) {
	if raw == "" {
		return "", ""
	}
	if len(raw) > maxBytes {
		return "", fmt.Sprintf("%s too large", label)
	}
	if !imageDataURIRe.MatchString(raw) {
		return "", fmt.Sprintf("%s must be a base64 data URI (png/jpeg/gif/webp)", label)
	}
	return raw, ""
}

func validatePost(req createPostRequest) (author, avatar, content, image, errMsg string) {
	author = sanitize(req.Author)
	content = sanitize(req.Content)
	if author == "" || content == "" {
		return "", "", "", "", "author and content are required"
	}
	if utf8.RuneCountInString(author) > maxAuthorLen {
		return "", "", "", "", fmt.Sprintf("author too long (max %d chars)", maxAuthorLen)
	}
	if utf8.RuneCountInString(content) > maxContentLen {
		return "", "", "", "", fmt.Sprintf("content too long (max %d chars)", maxContentLen)
	}
	avatar, errMsg = validateDataURI(req.Avatar, maxAvatarBytes, "avatar")
	if errMsg != "" {
		return "", "", "", "", errMsg
	}
	image, errMsg = validateDataURI(req.Image, maxImageBytes, "image")
	if errMsg != "" {
		return "", "", "", "", errMsg
	}
	return author, avatar, content, image, ""
}

type rateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int
	window   time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{requests: make(map[string][]time.Time), limit: limit, window: window}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	times := rl.requests[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= rl.limit {
		rl.requests[ip] = valid
		return false
	}
	rl.requests[ip] = append(valid, now)
	return true
}
