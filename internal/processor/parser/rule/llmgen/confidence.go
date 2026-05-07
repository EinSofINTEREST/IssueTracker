package llmgen

import (
	"reflect"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"issuetracker/internal/storage"
)

// confidenceThreshold 는 selector 의 hit_rate 가 본 임계값 미만이면 drop (nil) 처리됩니다 (이슈 #283).
//
// 단일 sample 환경에서 hit_rate 는 0.0 또는 1.0 (binary) — threshold 0.5 면 \"매칭 못함\" 만 drop.
// 향후 multi-sample 도입 시 (sample_count > 1) 임계값이 의미 있는 분기점이 됨.
const confidenceThreshold = 0.5

// publishedAtTimeLayouts 는 published_at 텍스트의 형식 검증에 사용할 time.Parse layout 후보입니다.
//
// 뉴스 사이트가 흔히 노출하는 datetime 형식 + ISO 8601 / RFC 3339 표준. 첫 매칭되는 layout 이
// hit 로 인정. layout 부족 시 운영자가 추가 (코드 변경 + 재배포 필요).
var publishedAtTimeLayouts = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02 15:04",
	"2006/01/02",
	"2006.01.02 15:04:05",
	"2006.01.02 15:04",
	"2006.01.02",
	"Mon, 02 Jan 2006 15:04:05 MST",   // RFC1123
	"Mon, 02 Jan 2006 15:04:05 -0700", // RFC1123Z
	"January 2, 2006",
	"Jan 2, 2006",
}

// ComputeFieldConfidence 는 SelectorMap 의 각 필드별 hit_rate 를 계산합니다 (이슈 #283).
//
// 단일 sample 환경 — 각 selector 를 html 에 적용 후 매칭 1건 이상이면 hit_rate=1.0, 아니면 0.0.
// SampleCount 는 항상 1 (multi-sample 도입 시 분모 증가).
//
// published_at 은 추가로 추출된 텍스트가 time.Parse 통과해야 hit 로 카운트 — selector 가 잘못된
// element (제목 / 광고 등) 매칭 시 텍스트는 추출되지만 date 가 아니므로 hit_rate=0.
//
// 빈 SelectorMap 또는 html parse 실패 시 빈 map 반환 (caller 가 무시).
func ComputeFieldConfidence(sm storage.SelectorMap, html string) map[string]storage.FieldConfidence {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return map[string]storage.FieldConfidence{}
	}
	out := make(map[string]storage.FieldConfidence)

	v := reflect.ValueOf(sm)
	t := reflect.TypeOf(sm)
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		// FieldSelector 포인터 필드만 (LinkDiscovery 등은 skip — pointer 가 아닌 별도 struct).
		if field.Kind() != reflect.Ptr || field.IsNil() {
			continue
		}
		fs, ok := field.Interface().(*storage.FieldSelector)
		if !ok {
			continue
		}
		jsonTag := jsonFieldName(t.Field(i).Tag.Get("json"))
		if jsonTag == "" {
			continue
		}
		hit := isFieldHit(doc, jsonTag, fs)
		out[jsonTag] = storage.FieldConfidence{
			HitRate:     boolToFloat(hit),
			SampleCount: 1,
		}
	}
	return out
}

// ApplyConfidenceFilter 는 confidence 가 임계값 미만인 필드의 selector 를 nil 로 drop 합니다 (이슈 #283).
//
// 신뢰할 수 없는 selector 가 INSERT 되면 하류 parser 가 빈 값을 추출 — host 별 \"본 필드 부재\" 학습
// 으로 이어짐. validator 는 confidence=0 인 필드를 \"부재가 정상\" 으로 판단할 수 있음 (sub-issue).
func ApplyConfidenceFilter(sm storage.SelectorMap, confidence map[string]storage.FieldConfidence) storage.SelectorMap {
	out := sm
	v := reflect.ValueOf(&out).Elem()
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if field.Kind() != reflect.Ptr || field.IsNil() {
			continue
		}
		jsonTag := jsonFieldName(t.Field(i).Tag.Get("json"))
		if jsonTag == "" {
			continue
		}
		c, ok := confidence[jsonTag]
		if !ok {
			continue
		}
		if c.HitRate < confidenceThreshold {
			field.Set(reflect.Zero(field.Type()))
		}
	}
	return out
}

// isFieldHit 는 doc 에 selector 를 적용해 hit (1건 이상 매칭 + 형식 검증 통과) 여부를 반환합니다.
//
// published_at 한정으로 추출된 텍스트가 time.Parse 통과해야 hit. 다른 필드는 단순 매칭만으로 hit.
func isFieldHit(doc *goquery.Document, fieldName string, fs *storage.FieldSelector) bool {
	if fs == nil || strings.TrimSpace(fs.CSS) == "" {
		return false
	}
	sel := doc.Find(fs.CSS)
	if sel.Length() == 0 {
		return false
	}
	if fieldName != "published_at" {
		return true
	}
	// published_at — 첫 매칭의 추출 결과가 date 로 parse 가능한지 확인.
	first := sel.First()
	var text string
	if fs.Attribute != "" {
		if v, exists := first.Attr(fs.Attribute); exists {
			text = strings.TrimSpace(v)
		}
	}
	if text == "" {
		text = strings.TrimSpace(first.Text())
	}
	if text == "" {
		return false
	}
	return tryParseDateLayouts(text)
}

// tryParseDateLayouts 는 publishedAtTimeLayouts 의 layout 을 순회하며 parse 성공 여부를 반환합니다.
func tryParseDateLayouts(text string) bool {
	for _, layout := range publishedAtTimeLayouts {
		if _, err := time.Parse(layout, text); err == nil {
			return true
		}
	}
	return false
}

// jsonFieldName 은 struct field tag 의 json 이름 부분만 추출합니다 (예: "title,omitempty" → "title").
//
// 빈 문자열 또는 "-" 면 무시 대상.
func jsonFieldName(tag string) string {
	if tag == "" || tag == "-" {
		return ""
	}
	if idx := strings.IndexByte(tag, ','); idx >= 0 {
		tag = tag[:idx]
	}
	return strings.TrimSpace(tag)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}
