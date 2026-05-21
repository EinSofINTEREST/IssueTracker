// Package queue 의 priority 헤더 파싱 유틸 (이슈 #524 — gemini #3278202670 DRY).
//
// Parser / Validate / Enrich worker 가 동일 로직 (Kafka 메시지 헤더의 "priority" 값을
// 1=high/2=normal/3=low int 로 파싱) 을 사용하므로 중복 함수를 공통 패키지로 이관.
package queue

import "strconv"

// PriorityHeaderKey 는 Kafka 메시지 헤더에 부착되는 priority 값의 키 이름입니다.
// bus.buildMessage 가 동일 키로 부착 — 본 상수와 일치해야 합니다.
const PriorityHeaderKey = "priority"

// PriorityFromHeader 는 Kafka 메시지 헤더의 "priority" 값을 int 로 파싱합니다.
//
// 매핑: 1=high / 2=normal / 3=low — core.Priority 와 동일.
// 미설정 / 파싱 실패 / 범위 밖 (1~3 외) 은 2 (PriorityNormal) 로 보정.
//
// 본 패키지가 core 의존을 피하기 위해 int 반환 — caller 가 필요시 core.Priority(v) 캐스팅.
// Parser / Validate / Enrich 의 동일 이름 함수는 본 함수로 이관 후 제거됨 (이슈 #524).
func PriorityFromHeader(headers map[string]string) int {
	v, ok := headers[PriorityHeaderKey]
	if !ok {
		return 2 // PriorityNormal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 3 {
		return 2
	}
	return n
}
