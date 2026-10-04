# 트레이싱

디버깅 및 분석을 위해 자세한 실행 추적을 캡처합니다. 추적에는 DOM 스냅샷, 스크린샷, 네트워크 활동 및 콘솔 로그가 포함됩니다.

## 기본 사용법

```bash
# Start trace recording
playwright-cli tracing-start

# Perform actions
playwright-cli open https://example.com
playwright-cli click e1
playwright-cli fill e2 "test"

# Stop trace recording
playwright-cli tracing-stop
```

## 추적 출력 파일

추적을 시작하면 Playwright는 여러 파일이 포함된 `traces/` 디렉터리를 생성합니다.

### `trace-{timestamp}.trace`

**작업 로그** - 다음을 포함하는 기본 추적 파일:
- 수행된 모든 작업(클릭, 채우기, 탐색)
- 각 작업 전후의 DOM 스냅샷
- 각 단계의 스크린샷
- 타이밍 정보
- 콘솔 메시지
- 소스 위치

### `trace-{timestamp}.network`

**네트워크 로그** - 전체 네트워크 활동:
- 모든 HTTP 요청 및 응답
- 요청 헤더 및 본문
- 응답 헤더 및 본문
- 타이밍(DNS, 연결, TLS, TTFB, 다운로드)
- 리소스 크기
- 실패한 요청 및 오류

### `resources/`

**리소스 디렉터리** - 캐시된 리소스:
- 이미지, 글꼴, 스타일시트, 스크립트
- 재생을 위한 응답 본문
- 페이지 상태를 재구성하는 데 필요한 자산

## 추적이 캡처하는 것

| 범주 | 세부 |
|----------|---------|
| **행위** | 클릭, 채우기, 마우스 오버, 키보드 입력, 탐색 |
| **돔** | 각 작업 전/후 전체 DOM 스냅샷 |
| **스크린샷** | 각 단계의 시각적 상태 |
| **회로망** | 모든 요청, 응답, 헤더, 본문, 타이밍 |
| **콘솔** | 모든 console.log, 경고, 오류 메시지 |
| **타이밍** | 각 작업의 정확한 타이밍 |

## 사용 사례

### 실패한 작업 디버깅

```bash
playwright-cli tracing-start
playwright-cli open https://app.example.com

# This click fails - why?
playwright-cli click e5

playwright-cli tracing-stop
# Open trace to see DOM state when click was attempted
```

### 성능 분석

```bash
playwright-cli tracing-start
playwright-cli open https://slow-site.com
playwright-cli tracing-stop

# View network waterfall to identify slow resources
```

### 증거 수집

```bash
# Record a complete user flow for documentation
playwright-cli tracing-start

playwright-cli open https://app.example.com/checkout
playwright-cli fill e1 "4111111111111111"
playwright-cli fill e2 "12/25"
playwright-cli fill e3 "123"
playwright-cli click e4

playwright-cli tracing-stop
# Trace shows exact sequence of events
```

## 추적 vs 비디오 vs 스크린샷

| 특징 | 추적하다 | 동영상 | 스크린샷 |
|---------|-------|-------|------------|
| **체재** | .trace 파일 | .webm 비디오 | .png/.jpeg 이미지 |
| **DOM 검사** | 예 | 아니요 | 아니요 |
| **네트워크 세부정보** | 예 | 아니요 | 아니요 |
| **단계별 재생** | 예 | 마디 없는 | 단일 프레임 |
| **파일 크기** | 중간 | 크기가 큰 | 작은 |
| **최적의 용도** | 디버깅 | 시민 | 빠른 캡처 |

## 모범 사례

### 1. 문제가 발생하기 전에 추적을 시작하세요

```bash
# Trace the entire flow, not just the failing step
playwright-cli tracing-start
playwright-cli open https://example.com
# ... all steps leading to the issue ...
playwright-cli tracing-stop
```

### 2. 오래된 흔적 정리

추적은 상당한 디스크 공간을 소비할 수 있습니다.

```bash
# Remove traces older than 7 days
find .playwright-cli/traces -mtime +7 -delete
```

## 제한 사항

- 추적은 자동화에 오버헤드를 추가합니다.
- 대규모 추적은 상당한 디스크 공간을 소비할 수 있습니다.
- 일부 동적 콘텐츠가 완벽하게 재생되지 않을 수 있습니다.
