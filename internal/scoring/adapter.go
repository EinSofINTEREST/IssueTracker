package scoring

import (
	"context"

	pgstore "issuetracker/internal/storage/postgres"
)

// pgAggregatorAdapter 는 postgres 집계기를 Aggregator 인터페이스로 잇습니다.
//
// 어댑터를 두는 이유: scoring 패키지가 storage/postgres 의 구체 타입에 의존하면
// 집계 원천을 바꿀 때 (예: cost/reliability 를 다른 곳에서 가져올 때) scorer 까지 고쳐야
// 합니다. 변환만 담당하는 얇은 층을 둡니다.
type pgAggregatorAdapter struct {
	inner *pgstore.HostSignalAggregator
}

// NewPostgresAggregator 는 postgres 집계기를 감싼 Aggregator 를 반환합니다.
func NewPostgresAggregator(inner *pgstore.HostSignalAggregator) Aggregator {
	return &pgAggregatorAdapter{inner: inner}
}

func (a *pgAggregatorAdapter) Aggregate(ctx context.Context, windowMinutes int) ([]HostAggregate, error) {
	rows, err := a.inner.Aggregate(ctx, windowMinutes)
	if err != nil {
		return nil, err
	}
	out := make([]HostAggregate, 0, len(rows))
	for _, r := range rows {
		out = append(out, HostAggregate{Host: r.Host, Signals: r.ToSignals()})
	}
	return out, nil
}
