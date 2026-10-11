package ingest

import (
	"strings"
	"testing"

	"synergy/internal/domain"
)

func TestCheckEnglish(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		english bool
	}{
		// English.
		{"english title", "Show HN: A tiny inference server for open-weight models", true},
		{"english abstract", "We propose a method for training language models with sparse attention. " +
			"Our approach reduces memory use and improves throughput on long-context benchmarks.", true},
		{"english with foreign name", "Le Chat by Mistral now supports deep research", true},
		{"english with a few cjk characters", "Token 超支？ Better Call Smith. Adaptive model routing for " +
			"coding agents with a usage dashboard and per-model budgets.", true},
		{"bilingual description", "nanaism/yomiyasu\nAI生成の日本語を自然な日本語へ推敲するAgent Skill / " +
			"Agent Skill for Refining AI-Generated Japanese text so it reads naturally", true},

		// Short or ambiguous: kept.
		{"empty", "", true},
		{"repo slug only", "owner/llm-router", true},
		{"version string", "Llama 4.1 (70B)", true},
		{"short spanish title", "La IA de Google", true},
		{"two foreign words", "Modelos para agentes", true},
		{"numbers and symbols", "2026 — 42% → 99.9%", true},
		{"single cjk word", "深度 model release notes", true},

		// Clearly not English.
		{"chinese", "Folkbench/folkbench\nFolkbench 是一个可信的 AI 中转站评测与服务目录，按模型比较可用率、响应速度、价格", false},
		{"japanese", "大規模言語モデルの推論を高速化する新しい手法", false},
		{"russian", "Новая открытая модель для генерации кода", false},
		{"spanish", "Un nuevo modelo de lenguaje para la generación de código que es más rápido y con menos memoria", false},
		{"german", "Ein neues Sprachmodell für die Codegenerierung, das schneller ist und mit weniger Speicher auskommt", false},
		{"french", "Un nouveau modèle de langage pour la génération de code qui est plus rapide et avec moins de mémoire", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkEnglish(tt.text)
			if (err == nil) != tt.english {
				t.Errorf("checkEnglish(%q) = %v, want english=%v", tt.text, err, tt.english)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "not English") {
				t.Errorf("error %q does not start with \"not English\"", err)
			}
		})
	}
}

func TestCheckEnglishSamplesOnlyTheStart(t *testing.T) {
	// English opening followed by a long non-English tail beyond the sample.
	text := strings.Repeat("An open model for code generation and agents. ", 40) + strings.Repeat("中文", 2000)
	if err := checkEnglish(text); err != nil {
		t.Errorf("checkEnglish = %v, want nil (tail is outside the sample)", err)
	}
}

func TestNormalizeRejectsNonEnglish(t *testing.T) {
	c := validCandidate()
	c.ExternalID = "43"
	c.Title = "owner/repo"
	c.Description = "一个用于大语言模型推理的高性能服务框架，支持多种模型"
	if _, err := Normalize(testSourceID, c, testNow); err == nil || !strings.Contains(err.Error(), "not English") {
		t.Fatalf("Normalize = %v, want a not-English rejection", err)
	}

	items, rejected, _ := normalizeBatch(testSourceID, []domain.Candidate{c, validCandidate()}, testNow)
	if len(items) != 1 || len(rejected) != 1 {
		t.Fatalf("normalizeBatch: %d items, %d rejected; want 1 and 1", len(items), len(rejected))
	}
}
