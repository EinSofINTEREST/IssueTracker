// storage 패키지는 데이터 접근 계층의 인터페이스와 공유 타입을 정의합니다.
// 구현체는 하위 패키지(postgres/)에 위치합니다.
package storage

import (
	"context"
	"errors"
)

// ErrNotFound는 요청한 레코드가 존재하지 않을 때 반환됩니다.
var ErrNotFound = errors.New("record not found")

// ErrDuplicate는 유일성 제약 위반(예: URL 중복) 시 반환됩니다.
var ErrDuplicate = errors.New("duplicate record")

// ErrInvalid 는 record 의 형식이 유효하지 않을 때 반환됩니다.
// 예: parser_rules.path_pattern 이 RE2 컴파일 실패 — Repository 가 DB write 전 거부.
var ErrInvalid = errors.New("invalid record")

// IsQueryTimeout 은 err 가 query-level timeout (이슈 #427) 으로 인한 실패인지 판단합니다.
//
// pgxpool 의 Acquire / Query / Exec / QueryRow / Begin 등이 ctx deadline 초과로
// 실패하면 context.DeadlineExceeded 가 fmt.Errorf 체인을 통해 전파됩니다. 본 helper 는
// 호출자가 그 시나리오를 식별하여 로그 / 메트릭 / 에러 코드 (core.CodeDBQueryTimeout, DB_002)
// 분류에 활용할 수 있도록 합니다.
//
// 사용 예:
//
//	if err := repo.Save(ctx, c); err != nil {
//	    if storage.IsQueryTimeout(err) {
//	        log.WithError(err).Warn("db query timed out")
//	        return core.NewDatabaseError(core.CodeDBQueryTimeout, "query timed out", true, err)
//	    }
//	    return err
//	}
func IsQueryTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}
