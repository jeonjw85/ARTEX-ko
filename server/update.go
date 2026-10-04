package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// 한 번의 클릭으로 HTTP 페이지가 업데이트되었습니다. 실제 다운로드/검증/교체 로직은 모두 selfupdate 패키지에 있습니다.
// 이는 인증 경계, 동시 상호 배제, 진행 브로드캐스트 및 main에 "종료 시간입니다"라고 알리는 역할만 담당합니다.
//
// 이 프로세스에서는 다시 시작이 완료되지 않습니다. 새 버전을 임시로 저장한 후 프로세스가 selfupdate.ExitRestart와 함께 종료됩니다.
// 데몬 스크립트(start.sh / start.bat, Docker 아래 ENTRYPOINT)에 의해 다시 시작되었습니다.

// restartCh는 업그레이드 준비가 완료되거나 롤백이 완료된 후 닫히고, main는 ExitRestart로 수신되어 종료됩니다.
var (
	restartOnce sync.Once
	restartCh   = make(chan struct{})
)

// RestartRequested "종료하고 데몬이 다시 나를 데리러 갈 수 있게 해주세요"라는 메시지가 나타나면 닫히는 channel를 반환합니다.
func RestartRequested() <-chan struct{} { return restartCh }

func requestRestart() { restartOnce.Do(func() { close(restartCh) }) }

// bootState는 이번 스타트업에서 selfupdate.Bootstrap의 결론입니다(업그레이드 성공/방금 롤백/
// 임시 파일은 폐기됨) main에 의해 삽입되므로 /api/update/check는 마지막 업그레이드 결과를 프런트 엔드에 진실하게 알릴 수 있습니다.
var (
	bootStateMu sync.Mutex
	bootState   selfupdate.State
)

// SetBootUpdateState는 시작 시 main에 의해 한 번 호출됩니다.
func SetBootUpdateState(st selfupdate.State) {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	bootState = st
}

func bootUpdateState() selfupdate.State {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	return bootState
}

// releaseCache는 GitHub의 최신 버전 쿼리 결과를 캐시합니다.
//
// 전체 페이지가 로드될 때마다 상단 표시줄의 "새 버전 사용 가능" 프롬프트가 확인되며, 인증되지 않은 GitHub API는
// 시간당 IP당 60회 - 캐싱이 없으면 탭을 몇 개 더 열거나 페이지를 새로 고치면 할당량이 소진됩니다.
// 나중에 정말 업데이트하고 싶었을 때 찾을 수 없었습니다. force는 사용자가 "업데이트 확인"을 명시적으로 클릭하면 캐시를 우회할 수 있습니다.
type releaseCache struct {
	mu  sync.Mutex
	rel *selfupdate.Release
	err error
	at  time.Time
	// fetch는 가져오기 기능으로, 테스트용으로 예약된 주입 지점일 뿐입니다. nil의 경우 실제 GitHub 쿼리가 실행됩니다.
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// 실패 결과도 짧은 시간 동안 캐시됩니다. 그렇지 않으면 GitHub에 연결할 수 없으면 페이지가 로드될 때마다 시간 초과를 기다려야 합니다.
	// 그러나 TTL는 더 짧으며 네트워크가 복원된 후 곧 복구됩니다.
	releaseErrTTL = 2 * time.Minute
	// 쿼리에 사용되는 시간 초과입니다. NewClient의 30분 제한 시간은 전체 패키지를 다운로드하기 위한 것입니다. 버전을 확인하는 데 그렇게 오래 기다릴 수는 없습니다.
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get는 최신 Release를 반환합니다. 캐시에 적중되면 네트워크에 액세스할 수 없습니다.
//
// 가져오기 기간 동안 잠금이 유지됩니다. 동시 요청은 각각 GitHub를 요청하는 대신 하나의 쿼리 결과를 기다리기 위해 대기열에 추가됩니다.
// (페이지가 처음 로드되면 여러 탭이 동시에 확인되는데, 이는 전류 제한이 발생할 가능성이 가장 높은 시간입니다.)
func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force {
		ttl := releaseTTL
		if c.err != nil {
			ttl = releaseErrTTL
		}
		if !c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch := c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatest
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err := fetch(ctx, client)
	// 요청이 취소되었다고 해서(사용자가 탭을 닫았다고 해서) GitHub에 문제가 있다는 의미는 아닙니다. 캐시에 쓰지 마십시오.
	// 그렇지 않으면 다음 방문자에게 설명할 수 없는 "취소됨" 오류가 표시됩니다.
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}

// updateProgress는 프런트 엔드로 푸시된 진행입니다.
type updateProgress struct {
	Phase   selfupdate.Phase `json:"phase"`
	Percent int              `json:"percent"` // 다운로드 단계만 중요합니다. 나머지는 -1이다
	Message string           `json:"message"`
	Version string           `json:"version,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// updateHub는 업그레이드 진행 상황을 보관하고 이를 SSE 가입자에게 방송합니다.
//
// running는 또한 뮤텍스 역할을 합니다: /api/update/apply direct 409 업그레이드 중에 다시 POST,
// 두 개의 goroutine가 동시에 동일한 artex.new에 쓰는 것을 방지합니다.
type updateHub struct {
	mu      sync.Mutex
	running bool
	cur     updateProgress
	subs    map[chan updateProgress]struct{}
}

var updHub = &updateHub{
	cur:  updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1},
	subs: map[chan updateProgress]struct{}{},
}

// begin는 업그레이드 권한을 선점합니다. 이미 진행 중인 경우 false를 반환합니다.
func (h *updateHub) begin(version string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	h.cur = updateProgress{Phase: selfupdate.PhaseDownload, Percent: 0, Message: "준비중…", Version: version}
	h.fanout(h.cur)
	return true
}

// finish가 업그레이드를 종료합니다. err는 nil로, 임시 저장이 성공했으며 다시 시작을 기다리고 있음을 나타냅니다.
func (h *updateHub) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	if err != nil {
		h.cur = updateProgress{Phase: selfupdate.PhaseFailed, Percent: -1, Message: "업데이트 실패", Error: err.Error(), Version: h.cur.Version}
	} else {
		h.cur = updateProgress{Phase: selfupdate.PhaseStaged, Percent: 100, Message: "새 버전이 준비되었습니다. 다시 시작합니다...", Version: h.cur.Version}
	}
	h.fanout(h.cur)
}

func (h *updateHub) publish(ph selfupdate.Phase, pct int, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = updateProgress{Phase: ph, Percent: pct, Message: msg, Version: h.cur.Version}
	h.fanout(h.cur)
}

// h.mu를 들고 있을 때 fanout를 호출해야 합니다. 구독자 channel는 버퍼링되어 가득 차면 삭제됩니다.
// 진행률은 삭제할 수 있는 일시적인 정보이며, SSE 연결이 중단되어도 업그레이드 자체가 차단되어서는 안 됩니다.
func (h *updateHub) fanout(p updateProgress) {
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func (h *updateHub) snapshot() (updateProgress, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur, h.running
}

func (h *updateHub) subscribe() (<-chan updateProgress, func()) {
	ch := make(chan updateProgress, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// updateCheck는 GitHub의 최신 공식 버전을 쿼리하고 현재 버전과 비교합니다.
//
// 프런트 엔드도 api.github.com(GitHub의 CORS는 *)에 직접 연결되지만 **이 인터페이스가 우선합니다**:
// 다운로드는 백엔드에서 이루어지며, 백엔드가 GitHub에 액세스할 수 있는 경우에만 업데이트할 수 있습니다. 브라우저는 연결할 수 있지만 서버는 연결할 수 없습니다.
// 상황은 매우 일반적이며(서버가 인트라넷에 있거나 에이전트가 브라우저에만 구성되어 있는 경우) 업데이트가 필연적으로 실패합니다.
// 확인 단계에서는 오류를 사실대로 보고하는 것이 좋습니다.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion
	mode := "binary"
	if selfupdate.InDocker() {
		mode = "docker"
	}
	boot := bootUpdateState()
	out := map[string]any{
		"current":     current,
		"mode":        mode,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"has_backup":  selfupdate.HasBackup(),
		"repo":        selfupdate.Repo,
		"boot_notice": boot.Detail,
		"rolled_back": boot.RolledBack,
	}

	// 상단 표시줄에는 캐시로 이동하라는 메시지가 표시됩니다(기본값). 사용자가 "업데이트 확인"을 클릭하면 force=1을 설정하여 소스로 강제로 돌아갑니다.
	force := r.URL.Query().Get("force") != ""
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, force)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}

	latest := rel.TagName
	out["latest"] = latest
	out["notes"] = rel.Body
	out["html_url"] = rel.HTMLURL
	if !rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Format(time.RFC3339)
	}

	asset := selfupdate.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	out["asset"] = asset
	if a, ok := rel.FindAsset(asset); ok {
		out["asset_available"] = true
		out["size"] = a.Size
	} else {
		out["asset_available"] = false
	}

	cmp, comparable := selfupdate.CompareVersions(current, latest)
	out["comparable"] = comparable
	out["has_update"] = comparable && cmp < 0
	if !comparable {
		// 개발 빌드(접미사가 있는 dev / git describe)에는 비슷한 버전 번호가 없습니다. 놓아주는 것은 단지
		// 공식 버전을 사용하여 디버깅 중인 로컬 바이너리를 덮어쓰면 직접 업데이트되지 않습니다.
		out["reason"] = fmt.Sprintf("현재 버전인 %q는 공식적으로 출시된 버전이 아니며 원클릭 업데이트가 비활성화되었습니다.", current)
	}
	writeJSON(w, 200, out)
}

// updateApply 새 버전을 다운로드하고 임시 저장하세요. 완료 후 프로세스가 종료되고 데몬 스크립트가 다시 시작됩니다.
//
// 즉시 202를 반환하고 실제 작업은 백그라운드에서 goroutine에서 실행됩니다. 전체 패키지를 다운로드하는 데 몇 분이 걸릴 수 있습니다.
// 요청 보류는 역방향 생성 시간 초과로 인해 중단됩니다. 진행 상황은 /api/update/stream입니다.
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion

	// 캐시 살펴보기: 설치된 버전이 사용자가 인터페이스에서 보고 확인하는 버전인지 확인하세요.
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if !comparable {
		writeErr(w, 400, fmt.Sprintf("현재 버전인 %q는 공식적으로 출시된 버전이 아니며 원클릭 업데이트가 비활성화되었습니다.", current))
		return
	}
	if cmp >= 0 {
		writeErr(w, 400, fmt.Sprintf("현재 최신 버전 %s입니다.", current))
		return
	}
	if !updHub.begin(rel.TagName) {
		writeErr(w, 409, "이미 업데이트가 진행 중입니다.")
		return
	}

	go func() {
		// 요청한 ctx 대신 의도적으로 s.ctx를 사용하십시오. HTTP 응답이 반환되는 즉시 요청이 종료됩니다.
		// 중단된 다운로드는 즉시 취소됩니다.
		err := selfupdate.Stage(s.ctx, client, rel, current, func(ph selfupdate.Phase, pct int, msg string) {
			updHub.publish(ph, pct, msg)
		})
		updHub.finish(err)
		if err != nil {
			log.Printf("[update] 업데이트 실패：%v", err)
			return
		}
		log.Printf("[update] %s → %s 일시적으로 저장되었으며 변경을 완료하기 위해 종료됩니다.", current, rel.TagName)
		// 마지막 진행 부분을 프런트 엔드로 푸시할 시간을 두고 종료를 트리거합니다.
		time.Sleep(1500 * time.Millisecond)
		requestRestart()
	}()

	writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName})
}

// updateRollback는 적극적으로 이전 버전(교체 전에 백업된 artex.old)으로 돌아갑니다.
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	if _, running := updHub.snapshot(); running {
		writeErr(w, 409, "업데이트가 진행 중이므로 롤백할 수 없습니다.")
		return
	}
	if err := selfupdate.Rollback(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[update] 이전 버전으로 수동으로 롤백하고 전환을 완료하기 위해 종료합니다.")
	writeJSON(w, 202, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		requestRestart()
	}()
}

// updateStream는 업데이트 진행 상황을 SSE로 푸시합니다.
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := updHub.subscribe()
	defer unsub()

	send := func(p updateProgress) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// 먼저 현재 상태를 추가하세요. 페이지를 새로 고치면 진행 중인 업그레이드를 즉시 확인할 수 있습니다.
	cur, _ := updHub.snapshot()
	send(cur)

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			send(p)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
