package selfupdate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// sumsAsset는 release.yml에 의해 생성된 체크섬 목록으로, Release의 모든 zip를 포함합니다.
const sumsAsset = "SHA256SUMS"

// maxBinarySize는 잘못된 zip가 디스크를 채우는 것을 방지하기 위해 압축이 풀린 바이너리 크기를 제한합니다.
const maxBinarySize = 512 << 20 // 512 MiB

// Phase는 업그레이드 프로세스의 한 단계이며 SSE 이벤트에서 phase 필드로 직접 사용됩니다.
type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseDownload Phase = "downloading"
	PhaseVerify   Phase = "verifying"
	PhaseExtract  Phase = "extracting"
	PhaseStaged   Phase = "staged"
	PhaseFailed   Phase = "failed"
)

// Progress는 호출자가 제공하며 진행 상황을 프런트 엔드로 푸시하는 데 사용됩니다. pct는 다운로드 단계(0-100)에서만 의미가 있습니다.
// 나머지 단계에서는 -1입니다.
type Progress func(ph Phase, pct int, msg string)

// Stage는 지정된 Release의 현재 플랫폼 릴리스 패키지를 다운로드하고 확인 후 새 바이너리를 artex.new로 임시 저장합니다.
//
// 두 가지 이유로 베어 바이너리 대신 완전한 zip가 사용됩니다. 기존 Release의 SHA256SUMS는 이미
// zip만 적용됩니다. zip를 사용하는 경우 CI를 변경할 필요가 없으며 출시된 이전 버전과도 호환됩니다. zip도 있습니다
// skills/, 내장된 skill의 향후 동기화를 위한 구멍을 남겨 둡니다. 가격은 skills와 몇백개의 KB를 다운로드하는 것뿐입니다.
//
// 함수가 반환되면 스테이징이 완료된 다음 호출자는 정상적으로 종료되고 ExitRestart로 종료됩니다.
func Stage(ctx context.Context, c *http.Client, rel *Release, currentVersion string, prog Progress) error {
	if prog == nil {
		prog = func(Phase, int, string) {}
	}
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if err := checkWritable(p.Dir); err != nil {
		return err
	}

	name := AssetName(rel.TagName, runtime.GOOS, runtime.GOARCH)
	asset, ok := rel.FindAsset(name)
	if !ok {
		return fmt.Errorf("이 버전은 %s/%s용 릴리스 패키지를 제공하지 않습니다(%s가 누락됨)", runtime.GOOS, runtime.GOARCH, name)
	}

	prog(PhaseDownload, 0, "체크섬 목록 가져오기…")
	sums, err := fetchSums(ctx, c, rel)
	if err != nil {
		return err
	}
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("%s는 %s에 포함되지 않으며, 검증되지 않은 바이너리의 설치는 거부됩니다.", sumsAsset, name)
	}

	// 모든 임시 파일은 대상 디렉터리에 있으므로 최종 rename가 동일한 파일 시스템에서 원자적 작업이 되도록 보장합니다.
	// (교차 장치 rename는 실패하지만 /tmp는 독립형 마운트 지점인 경우가 많습니다.)
	zipPath := p.New + ".zip.part"
	binPath := p.New + ".part"
	defer func() {
		_ = os.Remove(zipPath)
		_ = os.Remove(binPath)
	}()

	prog(PhaseDownload, 0, fmt.Sprintf("%s(%s) 다운로드…", name, humanSize(asset.Size)))
	got, err := download(ctx, c, asset, zipPath, prog)
	if err != nil {
		return err
	}

	prog(PhaseVerify, -1, "검증 SHA256…")
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA256 불일치: 예상된 %s, 실제 %s(다운로드가 손상되었거나 변조됨)", short(want), short(got))
	}

	prog(PhaseExtract, -1, "압축을 풀고 연기 테스트…")
	if err := extractBinary(zipPath, binPath); err != nil {
		return err
	}
	if err := smokeTest(binPath); err != nil {
		return fmt.Errorf("새 버전은 현재 시스템에서 실행할 수 없습니다: %w", err)
	}

	// 임시 파일 sha256의 별도 사본을 저장하십시오. 다음에 교체를 시작하기 전에 다시 확인해야 합니다.
	// 이렇게 하면 파일이 임시로 저장된 후 다시 시작되기 전까지 파일이 변경되거나 잘못 기록되는 것을 방지할 수 있습니다.
	binSum, err := fileSHA256(binPath)
	if err != nil {
		return fmt.Errorf("새로운 바이너리 체크섬 계산: %w", err)
	}
	if err := os.WriteFile(p.Sum, []byte(binSum), 0o644); err != nil {
		return fmt.Errorf("체크섬 쓰기: %w", err)
	}
	if err := os.Rename(binPath, p.New); err != nil {
		_ = os.Remove(p.Sum)
		return fmt.Errorf("임시 새 버전: %w", err)
	}

	if err := writeMarker(p.Marker, marker{
		From:     currentVersion,
		To:       strings.TrimPrefix(rel.TagName, "v"),
		StagedAt: time.Now().Unix(),
	}); err != nil {
		// 표시는 자동 롤백 기능에만 영향을 미치며, 임시 파일 자체는 이미 존재하므로 업그레이드가 중단되지 않습니다.
		prog(PhaseStaged, -1, "경고: 업그레이드 표시를 쓰지 못했습니다. 이 업그레이드에는 자동 롤백 보호 기능이 없습니다.")
	}

	prog(PhaseStaged, 100, "새 버전이 준비되었습니다. 다시 시작합니다...")
	return nil
}

// fetchSums SHA256SUMS를 다운로드하고 구문 분석하여 파일 이름 → 16진수 다이제스트를 반환합니다.
func fetchSums(ctx context.Context, c *http.Client, rel *Release) (map[string]string, error) {
	asset, ok := rel.FindAsset(sumsAsset)
	if !ok {
		return nil, fmt.Errorf("Release에는 %s가 없으며 무결성을 확인할 수 없으며 업그레이드가 거부됩니다.", sumsAsset)
	}
	body, err := get(ctx, c, asset.URL)
	if err != nil {
		return nil, fmt.Errorf("%s 다운로드: %w", sumsAsset, err)
	}
	defer body.Close()

	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%s 읽기: %w", sumsAsset, err)
	}
	out := parseSums(string(raw))
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 내용이 비어 있거나 형식을 인식할 수 없습니다.", sumsAsset)
	}
	return out, nil
}

// parseSums sha256sum 스타일 목록을 구문 분석하여 파일 이름 → 16진수 다이제스트를 반환합니다.
//
// 첫 번째 필드는 64자리 16진수여야 포함됩니다. 단지 "정확히 두 개의 필드"로 판단하는 것만으로는 충분하지 않습니다.
// 한 줄에 있는 두 단어로 된 설명 텍스트는 합법적인 항목으로 처리되며 쓰레기 값은 요약 테이블에 채워집니다.
// 대신 실제 자산이 잘못된 다이제스트와 일치할 수 있습니다.
func parseSums(raw string) map[string]string {
	out := map[string]string{}
	for line := range strings.Lines(raw) {
		// 형식은 "<sha256> <filename>"입니다(sha256sum는 이중 공백을 사용하고 shasum는 바이너리를 사용합니다).
		// 모드는 파일 이름 앞에 *)를 붙입니다.
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || !isHexSHA256(fields[0]) {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == "" {
			continue
		}
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// download는 자산을 dst에 쓰고 동시에 SHA256를 계산하며 Content-Length에 따라 진행 상황을 보고합니다.
func download(ctx context.Context, c *http.Client, a Asset, dst string, prog Progress) (string, error) {
	body, err := get(ctx, c, a.URL)
	if err != nil {
		return "", fmt.Errorf("%s 다운로드: %w", a.Name, err)
	}
	defer body.Close()

	f, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("임시 파일 생성: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	pw := &progressWriter{total: a.Size, prog: prog, name: a.Name, last: time.Now()}
	if _, err := io.Copy(io.MultiWriter(f, h, pw), body); err != nil {
		return "", fmt.Errorf("다운로드 중단됨: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("주문 실패: %w", err)
	}
	if a.Size > 0 && pw.written != a.Size {
		return "", fmt.Errorf("불완전한 다운로드: 예상 %d 바이트, 실제 %d 바이트", a.Size, pw.written)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// get는 화이트리스트에 따라 GET를 시작하고 응답 본문을 반환합니다.
func get(ctx context.Context, c *http.Client, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-selfupdate")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// extractBinary 릴리스 패키지에서 artex 실행 파일을 꺼냅니다.
//
// 패키지의 구조는 artex-<version>-<os>-<arch>/artex이지만 여기서는 완전히 철자를 사용하는 대신 **기본 이름**과 일치합니다.
// 경로: 버전 번호는 패키지 이름에 한 번 표시됩니다. 한 문자의 철자를 틀리면 전체 업그레이드가 실패합니다. 더 강력한 버전을 찾으려면 기본 이름을 사용하세요.
func extractBinary(zipPath, dst string) error {
	want := "artex"
	if runtime.GOOS == "windows" {
		want = "artex.exe"
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("릴리스 패키지 열기: %w", err)
	}
	defer zr.Close()

	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() || !strings.EqualFold(path.Base(entry.Name), want) {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return fmt.Errorf("%s 읽기: %w", entry.Name, err)
		}
		defer rc.Close()

		f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return fmt.Errorf("새 바이너리 작성: %w", err)
		}
		defer f.Close()

		n, err := io.Copy(f, io.LimitReader(rc, maxBinarySize+1))
		if err != nil {
			return fmt.Errorf("%s 압축 풀기: %w", entry.Name, err)
		}
		if n > maxBinarySize {
			return fmt.Errorf("릴리스 패키지의 실행 파일이 %s를 초과하여 압축 해제가 거부됩니다.", humanSize(maxBinarySize))
		}
		if n == 0 {
			return fmt.Errorf("릴리스 패키지의 %s는 빈 파일입니다.", want)
		}
		return f.Sync()
	}
	return fmt.Errorf("릴리스 패키지에서 %s를 찾을 수 없습니다.", want)
}

// checkWritable 디렉토리에 쓰기 가능한지 미리 확인하십시오. 이 단계가 없으면 root가 아닌 것이 실행되거나 바이너리가 시스템에 배치됩니다.
// 디렉토리에 수십 개의 MB를 다운로드한 후 재설치하는 순간 실패합니다.
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".artex-update-probe-*")
	if err != nil {
		return fmt.Errorf("프로그램 디렉터리 %s는 쓸 수 없으며 자동으로 업데이트할 수 없습니다(대신 권한을 확인하거나 수동 업그레이드를 사용하십시오): %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// progressWriter는 쓰여진 바이트 수를 계산하고 제한된 빈도로 보고하여 각 32KiB 블록에 대해 하나의 SSE를 푸시하지 않도록 합니다.
type progressWriter struct {
	total   int64
	written int64
	name    string
	prog    Progress
	last    time.Time
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.written += int64(len(b))
	if time.Since(w.last) < 300*time.Millisecond {
		return len(b), nil
	}
	w.last = time.Now()
	pct := -1
	if w.total > 0 {
		pct = int(w.written * 100 / w.total)
	}
	w.prog(PhaseDownload, pct, fmt.Sprintf("%s / %s 다운로드 중", humanSize(w.written), humanSize(w.total)))
	return len(b), nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12] + "…"
	}
	return sum
}
