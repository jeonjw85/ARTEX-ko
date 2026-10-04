package server

import (
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// 재시도 전략에 대한 서버 측 분석은 docs/LLM 재시도 설계.md를 참조하세요. 5층:
//   - 연결 설정/빈 응답/provider 보안 창과 동일 "엔드포인트를 따르며" 각 LLM 구성에 의해 재정의될 수 있음
//     전역 기본값(profile의 항목이 공백으로 남아 있으면 전역 값을 상속합니다. 전역적으로 구성되지 않으면 내장된 기본값이 사용됩니다)
//   - 회로 차단기/의도된 재실행은 프로세스 수준에 있으며 전역 복사본만 있습니다.
//
// 글로벌 전략은 DB와 settings의 한 줄을 읽고 콜 포인트는 모두 저주파 경로에 있습니다(빌드 provider, work 엔딩,
// 구성 저장) 다른 캐시 레이어를 추가할 가치가 없습니다. 예외는 회로 차단기 매개변수입니다. 이는 오류 경로에서 매번 읽어야 하므로
// applyRetryPolicy에서 Registry로 저장했습니다.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// 여기서의 횟수는 "0=default/negative=off"의 원래 의미를 유지합니다: MaxRetries of SDK /
		// EmptyResponseRetries는 완전히 동형이므로 자체적으로 분석하도록 놔두세요.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// 퓨즈(폴링 냉각)의 기본값은 내장된 llmpool 값과 일치합니다. 여기서는 "사용자가 값을 구성"하는 경우에만 재정의됩니다.
// 인텐트 재실행의 기본값은 engine.go의 modelErrorRetries / modelErrorRetryBackoff를 참조하세요.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob —— "빈 응답
// "재시도 횟수": 둘 다 동일한 작업을 수행하는 두 가지 방법입니다. SDK 해당 관리 계층에는 "단일 콘텐츠 블록이 없습니다". 방법은
// 동일한 요청이 그대로 재발행됩니다. 여기서는 "생각만 하고, 텍스트도 도구도 아닌" 것을 의미합니다. 방법은 명령을 추가하는 것입니다.
// 모델은 기존 생각을 계속 이어갑니다(컨텍스트 모양에 따라 결정되는 이러한 종류의 유휴 상태에 대해서는 있는 그대로 다시 보내는 것은 의미가 없습니다). 짧은 구경
// 차이점은 SDK는 "yield 이벤트가 발생했는지 여부"를 기반으로 하는 반면, 증가 자체를 이벤트로 생각하는 반면 사용자 구성은
// "빈 응답을 몇 번 재시도한다"라고 표현하고 싶은 것은 "모델이 실질적인 내용을 생산하지 못하면 다시 시도한다"는 것이다. 두 레이어는 동일한 횟수를 공유합니다.
// 그래야만 이 사고방식과 일치할 수 있습니다.
//
// 특정 profile에 대한 적용 범위 대신 글로벌 정책을 읽어 보십시오. 장애 조치로 인해 run가 profile로 대체될 수 있으며 이는
// 이는 전체 의도의 총량이며 끝점의 변경에 따라 변경되어서는 안 됩니다. 의미 체계는 SDK의 emptyRetries()와 동형입니다.
// 0 = 기본 defaultEmptyTurnNudges; -1(음수) = 공회전을 끄고 계속 작동합니다. >0 = 이 값을 사용합니다.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
