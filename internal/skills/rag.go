package skills

import (
	"math"
	"sort"
	"strings"
)

// SkillMatch — результат поиска с релевантностью
type SkillMatch struct {
	Skill     *Skill
	Score     float64
	MatchType string // keyword, semantic, trigger
}

// FindRelevant — RAG поиск по запросу, возвращает topK скиллов
func (h *HotLoader) FindRelevant(query string, topK int) []SkillMatch {
	h.mu.RLock()
	defer h.mu.RUnlock()

	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)

	var matches []SkillMatch

	for _, skill := range h.skills {
		score := 0.0
		matchType := ""

		// 1. Always-active скиллы — максимальный приоритет
		if skill.TriggerMode == "always" {
			score = 100.0
			matchType = "always"
			matches = append(matches, SkillMatch{Skill: skill, Score: score, MatchType: matchType})
			continue
		}

		// 2. Keyword matching (TF-IDF-like)
		for _, kw := range skill.Keywords {
			kwLower := strings.ToLower(kw)
			if strings.Contains(queryLower, kwLower) {
				score += 5.0
				matchType = "keyword"
			}
			// Частичное совпадение слов
			for _, qw := range queryWords {
				if strings.Contains(kwLower, qw) || strings.Contains(qw, kwLower) {
					score += 2.0
					if matchType == "" {
						matchType = "partial"
					}
				}
			}
		}

		// 3. Description matching
		if skill.Description != "" {
			descLower := strings.ToLower(skill.Description)
			for _, qw := range queryWords {
				if strings.Contains(descLower, qw) {
					score += 1.5
				}
			}
		}

		// 4. Name matching
		nameLower := strings.ToLower(skill.Name)
		for _, qw := range queryWords {
			if strings.Contains(nameLower, qw) {
				score += 3.0
				if matchType == "" {
					matchType = "name"
				}
			}
		}

		// 5. Content relevance (простой check)
		contentLower := strings.ToLower(skill.Content)
		for _, qw := range queryWords {
			if strings.Contains(contentLower, qw) {
				score += 0.5
			}
		}

		if score > 0 {
			matches = append(matches, SkillMatch{Skill: skill, Score: score, MatchType: matchType})
		}
	}

	// Сортируем по релевантности
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Score > matches[j].Score
	})

	// Возвращаем topK
	if len(matches) > topK {
		matches = matches[:topK]
	}

	return matches
}

// BuildPromptRelevant — строит промпт только из релевантных скиллов
func (h *HotLoader) BuildPromptRelevant(query string, topK int) string {
	matches := h.FindRelevant(query, topK)
	if len(matches) == 0 {
		return ""
	}

	var parts []string
	parts = append(parts, "Relevant skills for this task:\n")

	for _, m := range matches {
		skill := m.Skill
		parts = append(parts, fmt.Sprintf("=== %s [%.0f] ===\n%s\n", skill.Name, m.Score, skill.Content))
	}

	return strings.Join(parts, "\n")
}

// CosineSimilarity — простая косинусная близость (можно заменить на embeddings)
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
