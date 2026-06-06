// internal/utils/search.go (фикс)
package utils

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SearchResult struct {
	Title   string
	Link    string
	Snippet string
}

func WebSearch(ctx context.Context, query string) ([]SearchResult, error) {
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parseDuckDuckGo(string(body)), nil
}

func WebFetch(ctx context.Context, pageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	content := string(body)
	if len(content) > 10000 {
		content = content[:10000] + "\n\n...[truncated]"
	}
	return content, nil
}

func parseDuckDuckGo(html string) []SearchResult {
	var results []SearchResult
	lines := strings.Split(html, "\n")
	var current ResultBuilder

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "result__a") {
			title := extractText(line)
			link := extractHref(line)
			if title != "" {
				current.Title = title
				current.Link = link
			}
		}
		if strings.Contains(line, "result__snippet") {
			snippet := extractText(line)
			if snippet != "" {
				current.Snippet = snippet
			}
		}
		if current.Title != "" && current.Snippet != "" {
			results = append(results, SearchResult{
				Title:   current.Title,
				Link:    current.Link,
				Snippet: current.Snippet,
			})
			current = ResultBuilder{}
		}
		if len(results) >= 5 {
			break
		}
	}
	return results
}

type ResultBuilder struct {
	Title   string
	Link    string
	Snippet string
}

func extractText(html string) string {
	var result strings.Builder
	inTag := false
	for _, r := range html {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return strings.TrimSpace(result.String())
}

func extractHref(html string) string {
	start := strings.Index(html, `href="`)
	if start == -1 {
		return ""
	}
	start += 6
	end := strings.Index(html[start:], `"`)
	if end == -1 {
		return ""
	}
	return html[start : start+end]
}

func FormatSearchResults(results []SearchResult, query string) string {
	if len(results) == 0 {
		return fmt.Sprintf("🔍 По запросу '%s' ничего не найдено", query)
	}
	output := fmt.Sprintf("🔍 *Результаты:* `%s`\n\n", query)
	for i, r := range results {
		output += fmt.Sprintf("*%d.* %s\n", i+1, r.Title)
		if r.Snippet != "" {
			output += fmt.Sprintf("_%s_\n", r.Snippet)
		}
		if r.Link != "" {
			output += fmt.Sprintf("[🔗 Ссылка](%s)\n", r.Link)
		}
		output += "\n"
	}
	return output
}
