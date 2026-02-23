// storage 패키지는 데이터 접근 계층의 인터페이스와 공유 타입을 정의합니다.
// 구현체는 하위 패키지(postgres/)에 위치합니다.
package storage

import "errors"

// ErrNotFound는 요청한 레코드가 존재하지 않을 때 반환됩니다.
var ErrNotFound = errors.New("record not found")

// ErrDuplicate는 유일성 제약 위반(예: URL 중복) 시 반환됩니다.
var ErrDuplicate = errors.New("duplicate record")
