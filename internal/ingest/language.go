package ingest

import (
	"fmt"
	"strings"
	"unicode"
)

// Language filtering. Synergy shows English content only, so candidates
// whose text is clearly in another language are rejected during
// normalization. The check is deliberately conservative: it rejects only on
// strong evidence and keeps short, mixed, ambiguous or unclassifiable text,
// because discarding English stories is worse than letting an occasional
// foreign one through. It needs no network, model or dependency.
//
// Two signals are used, over the title plus the start of the description:
//
//  1. Script: when most letters (CJK characters weighted, see cjkWeight)
//     are outside the Latin script (Chinese, Japanese, Cyrillic, Arabic,
//     ...) and there are enough of them.
//  2. Function words: for Latin-script text, frequent words that are
//     distinctive of other European languages ("und", "pour", "para")
//     clearly outnumber English ones ("the", "and", "for").
const (
	// languageSampleRunes bounds how much text is examined.
	languageSampleRunes = 1500
	// minForeignScriptLetters is the fewest non-Latin letters that can
	// reject a candidate, so a stray name or symbol never does.
	minForeignScriptLetters = 4
	// cjkWeight counts each Chinese, Japanese or Korean character as this
	// many letters: one carries about as much text as a short English word,
	// so Latin repository slugs and product names cannot outweigh a
	// description written in Chinese.
	cjkWeight = 2
	// minForeignWords is the fewest foreign function words that can reject
	// Latin-script text; short titles rarely reach it and are kept.
	minForeignWords = 3
)

// englishWords are frequent English function words.
var englishWords = wordSet(`the and of to is are was were for with that this from by an it its as on
	at we our you your how what why new using into can not be has have which or but their they all
	about more than will just been when who use via based towards toward`)

// foreignWords are frequent function words of Spanish, French, German,
// Portuguese, Italian, Dutch and Swedish that are not also common English
// words (so "die", "a", "in", "an", "de" and "o" are excluded).
var foreignWords = wordSet(`el la los las del que por para con una uno es y se su al como más muy pero
	le les des du et est une pour dans sur pas aux avec qui cette sont mais très
	der das und ist nicht mit für ein eine einen zu auf den dem von sich auch wir ich sie wie oder
	não uma com em é são também
	il di della dello che sono gli è anche
	het een van niet voor zijn ook
	och att är för på inte`)

func wordSet(s string) map[string]bool {
	m := make(map[string]bool)
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// checkEnglish returns an error when text is clearly not English, and nil
// when it is English or the evidence is too weak to tell.
func checkEnglish(text string) error {
	r := []rune(text)
	if len(r) > languageSampleRunes {
		r = r[:languageSampleRunes]
	}
	sample := string(r)

	var latin, foreign int
	for _, c := range sample {
		switch {
		case !unicode.IsLetter(c):
		case unicode.Is(unicode.Latin, c):
			latin++
		case unicode.In(c, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			foreign += cjkWeight
		default:
			foreign++
		}
	}
	if foreign >= minForeignScriptLetters && foreign > latin {
		return fmt.Errorf("not English: mostly non-Latin script (weight %d against %d Latin letters)", foreign, latin)
	}

	var en, fo int
	for _, w := range strings.FieldsFunc(strings.ToLower(sample), func(c rune) bool { return !unicode.IsLetter(c) }) {
		switch {
		case englishWords[w]:
			en++
		case foreignWords[w]:
			fo++
		}
	}
	if fo >= minForeignWords && fo > 2*en {
		return fmt.Errorf("not English: %d non-English and %d English function words", fo, en)
	}
	return nil
}
