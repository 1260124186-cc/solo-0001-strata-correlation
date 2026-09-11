package geology

import (
	"sort"
	"strings"
)

// MarkerNameMax is the length limit of a marker bed name.
const MarkerNameMax = 80

// MaxGlossaryEntries bounds the persisted marker glossary.
const MaxGlossaryEntries = 5000

// MarkerKey is the single name-equivalence predicate of the whole service:
// intra-profile duplicate rejection, offset suggestion matching and
// comparison evidence matching must all use it, so their conclusions can
// never disagree.
//
// Equivalence ignores surrounding whitespace, letter case and the
// full-width/half-width distinction: ASCII letters, digits, punctuation and
// spaces fold to their ASCII forms, half-width katakana fold to full-width
// katakana (including voiced/pseudo-voiced composition), and the result is
// Unicode-lower-cased. Anything outside those mappings (notably CJK
// characters) is preserved verbatim.
func MarkerKey(name string) string {
	return strings.ToLower(foldWidth(strings.TrimSpace(name)))
}

// foldWidth is the width-folding step of MarkerKey.
func foldWidth(s string) string {
	folded := make([]rune, 0, len(s))
	changed := false
	for _, r := range s {
		switch {
		case r == 0x3000:
			folded = append(folded, ' ')
			changed = true
		case r >= 0xFF01 && r <= 0xFF5E:
			folded = append(folded, rune(r-0xFEE0))
			changed = true
		case r >= 0xFF61 && r <= 0xFF9F:
			folded = append(folded, foldHalfWidthKatakana(r)...)
			changed = true
		default:
			folded = append(folded, r)
		}
	}
	if changed {
		return string(composeKatakanaMarks(folded))
	}
	return s
}

// halfWidthKatakana maps the FF61..FF9F block to full-width katakana.
var halfWidthKatakana = map[rune]rune{
	0xFF61: 0x3002, // ｡
	0xFF62: 0x300C, // ｢
	0xFF63: 0x300D, // ｣
	0xFF64: 0x3001, // ､
	0xFF65: 0x30FB, // ･
	0xFF66: 0x30F2, // ｦ→ヲ
	0xFF67: 0x30A1, // ｧ→ァ
	0xFF68: 0x30A3, // ｨ
	0xFF69: 0x30A5, // ｩ
	0xFF6A: 0x30A7, // ｪ
	0xFF6B: 0x30A9, // ｫ
	0xFF6C: 0x30E3, // ｬ
	0xFF6D: 0x30E5, // ｭ
	0xFF6E: 0x30E7, // ｮ
	0xFF6F: 0x30C3, // ｯ
	0xFF70: 0x30FC, // ｰ
	0xFF71: 0x30A2, // ｱ
	0xFF72: 0x30A4, // ｲ
	0xFF73: 0x30A6, // ｳ
	0xFF74: 0x30A8, // ｴ
	0xFF75: 0x30AA, // ｵ
	0xFF76: 0x30AB, // ｶ
	0xFF77: 0x30AD, // ｷ
	0xFF78: 0x30AF, // ｸ
	0xFF79: 0x30B1, // ｹ
	0xFF7A: 0x30B3, // ｺ
	0xFF7B: 0x30B5, // ｻ
	0xFF7C: 0x30B7, // ｼ
	0xFF7D: 0x30B9, // ｽ
	0xFF7E: 0x30BB, // ｾ
	0xFF7F: 0x30BD, // ｿ
	0xFF80: 0x30BF, // ﾀ
	0xFF81: 0x30C1, // ﾁ
	0xFF82: 0x30C4, // ﾂ
	0xFF83: 0x30C6, // ﾃ
	0xFF84: 0x30C8, // ﾄ
	0xFF85: 0x30CA, // ﾅ
	0xFF86: 0x30CB, // ﾆ
	0xFF87: 0x30CC, // ﾇ
	0xFF88: 0x30CD, // ﾈ
	0xFF89: 0x30CE, // ﾉ
	0xFF8A: 0x30CF, // ﾊ
	0xFF8B: 0x30D2, // ﾋ
	0xFF8C: 0x30D5, // ﾌ
	0xFF8D: 0x30D8, // ﾍ
	0xFF8E: 0x30DB, // ﾎ
	0xFF8F: 0x30DE, // ﾏ
	0xFF90: 0x30DF, // ﾐ
	0xFF91: 0x30E0, // ﾑ
	0xFF92: 0x30E1, // ﾒ
	0xFF93: 0x30E2, // ﾓ
	0xFF94: 0x30E4, // ﾔ
	0xFF95: 0x30E6, // ﾕ
	0xFF96: 0x30E8, // ﾖ
	0xFF97: 0x30E9, // ﾗ
	0xFF98: 0x30EA, // ﾘ
	0xFF99: 0x30EB, // ﾙ
	0xFF9A: 0x30EC, // ﾚ
	0xFF9B: 0x30ED, // ﾛ
	0xFF9C: 0x30EF, // ﾜ→ワ
	0xFF9D: 0x30F3, // ﾝ
}

// voicedComposed and semiVoicedComposed resolve a half-width katakana base
// followed by a separate ﾞ/ﾟ mark into its single composed full-width rune.
var voicedComposed = map[rune]rune{
	0x30A6: 0x30F4, // ウ→ヴ
	0x30AB: 0x30AC, // カ→ガ
	0x30AD: 0x30AE, // キ→ギ
	0x30AF: 0x30B0, // ク→グ
	0x30B1: 0x30B2, // ケ→ゲ
	0x30B3: 0x30B4, // コ→ゴ
	0x30B5: 0x30B6, // サ→ザ
	0x30B7: 0x30B8, // シ→ジ
	0x30B9: 0x30BA, // ス→ズ
	0x30BB: 0x30BC, // セ→ゼ
	0x30BD: 0x30BE, // ソ→ゾ
	0x30BF: 0x30C0, // タ→ダ
	0x30C1: 0x30C2, // チ→ヂ
	0x30C4: 0x30C5, // ツ→ヅ
	0x30C6: 0x30C7, // テ→デ
	0x30C8: 0x30C9, // ト→ド
	0x30CF: 0x30D0, // ハ→バ
	0x30D2: 0x30D3, // ヒ→ビ
	0x30D5: 0x30D6, // フ→ブ
	0x30D8: 0x30D9, // ヘ→ベ
	0x30DB: 0x30DC, // ホ→ボ
	0x30EF: 0x30F7, // ワ→ヷ
	0x30F2: 0x30FA, // ヲ→ヺ
	0x30A8: 0x30F9, // エ→ヹ
}

var semiVoicedComposed = map[rune]rune{
	0x30CF: 0x30D1, // ハ→パ
	0x30D2: 0x30D4, // ヒ→ピ
	0x30D5: 0x30D7, // フ→プ
	0x30D8: 0x30DA, // ヘ→ペ
	0x30DB: 0x30DD, // ホ→ポ
}

// foldHalfWidthKatakana folds one half-width katakana rune to full-width.
// The standalone voiced and pseudo-voiced marks become ゛/゜ and are merged
// with the preceding base by composeKatakanaMarks.
func foldHalfWidthKatakana(r rune) []rune {
	if mapped, ok := halfWidthKatakana[r]; ok {
		return []rune{mapped}
	}
	if r == 0xFF9E { // ﾞ
		return []rune{0x309B} // ゛
	}
	if r == 0xFF9F { // ﾟ
		return []rune{0x309C} // ゜
	}
	return []rune{r}
}

// composeKatakanaMarks applies a trailing ﾞ/ﾟ (already folded to ゛/゜) to
// the previous full-width katakana base when a composed form exists.
func composeKatakanaMarks(runes []rune) []rune {
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if i > 0 && (r == 0x309B || r == 0x309C) {
			base := out[len(out)-1]
			var composed rune
			var ok bool
			if r == 0x309B {
				composed, ok = voicedComposed[base]
			} else {
				composed, ok = semiVoicedComposed[base]
			}
			if ok {
				out[len(out)-1] = composed
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// LegacyMarkers records, per equivalence key, the distinct raw spellings that
// already coexisted in historical revisions. Such collisions were legal
// under the old case-only rule (or slipped in before this service existed)
// and are grandfathered on load and on further edits of that profile, while
// no fresh collision is ever accepted.
type LegacyMarkers map[string]map[string]struct{}

// Add records a raw spelling under an equivalence key.
func (l LegacyMarkers) Add(key, spelling string) {
	set := l[key]
	if set == nil {
		set = map[string]struct{}{}
		l[key] = set
	}
	set[spelling] = struct{}{}
}

// Merge folds another legacy report into this one.
func (l LegacyMarkers) Merge(other LegacyMarkers) {
	for key, set := range other {
		for spelling := range set {
			l.Add(key, spelling)
		}
	}
}

// Allows reports whether every given spelling is part of the grandfathered
// spelling set of a known colliding key.
func (l LegacyMarkers) Allows(key string, spellings ...string) bool {
	if l == nil {
		return false
	}
	set, ok := l[key]
	if !ok || len(set) < 2 {
		return false
	}
	for _, spelling := range spellings {
		if _, ok := set[spelling]; !ok {
			return false
		}
	}
	return true
}

// Keys returns the equivalence keys in stable order.
func (l LegacyMarkers) Keys() []string {
	keys := make([]string, 0, len(l))
	for key := range l {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Spellings returns the grandfathered raw spellings of a key in stable order.
func (l LegacyMarkers) Spellings(key string) []string {
	out := make([]string, 0, len(l[key]))
	for spelling := range l[key] {
		out = append(out, spelling)
	}
	sort.Strings(out)
	return out
}

// ScanLegacy finds marker keys carrying more than one distinct raw spelling
// within one revision, i.e. collisions the unified MarkerKey rule would now
// reject but historical data already contains.
func ScanLegacy(layers []Layer) LegacyMarkers {
	spellings := map[string]map[string]struct{}{}
	for _, layer := range layers {
		if layer.Marker == "" {
			continue
		}
		key := MarkerKey(layer.Marker)
		set := spellings[key]
		if set == nil {
			set = map[string]struct{}{}
			spellings[key] = set
		}
		set[layer.Marker] = struct{}{}
	}
	legacy := LegacyMarkers{}
	for key, set := range spellings {
		if len(set) > 1 {
			legacy[key] = set
		}
	}
	return legacy
}
