---
name: api-recon
description: 웹사이트 API 인터페이스 수집 시 호출되는 스킬입니다.
---

# API Recon(프런트 엔드 인터페이스 정찰)

**권한 부여**를 전제로 **백엔드 API**(경로, 메서드, 매개 변수, 응답 본문), **프런트 엔드 라우팅**, **UI 기능 트리거 포인트**(Tab, 팝업 창, 테이블 작업 등)를 최대한 완벽하게 검색합니다.

---

## 경계 및 금지 사항(Agent 반드시 읽어야 함 · 위반은 경계를 넘는 것을 의미함)

이 skill는 **API/매개변수 정찰**만 수행하며 취약점 마이닝 또는 침투 악용 단계는 수행하지 않습니다.

### 작업 경계

| 범위 | 허용 | 금지하다 |
|---|---|---|
| **목표** | path, method, 매개변수, 경로, UI 트리거 포인트를 열거합니다. | SQLi/XSS/ 재정의/폭발/fuzz 취약점, 패키지 수정 공격, 파괴적인 작업 |
| **입증** | Hook + stub/mock는 **클라이언트** 로그인 게이트를 우회합니다. | 사용자에게 계정 비밀번호를 묻거나 추측합니다. 실제 로그인 양식을 제출해 보세요 |
| **실행 시간** | 자격 증명 없이 hook 인터페이스를 사용하고 mock 응답을 사용하여 SPA가 로그인 후 셸에 들어갈 수 있도록 허용합니다. | 계속하기 위해 실제 백엔드 세션에 의존하는 프로세스 |

### 자격 증명 없이 동적 분석(Phase 3 기본값)

1. `preload.js` / `runtime_harvest.js` ** 및 stub** 로그인, 권한, 메뉴 등을 통해 가로채기. bootstrap 인터페이스;
2. 비즈니스 쿼리 인터페이스는 올바른 구조, 성공적인 비즈니스 코드 및 빈 데이터**가 포함된 mock body를 반환합니다.
3. 백엔드 없이 또는 401 환경에서 로그인 후 페이지를 계속 렌더링하도록 프런트 엔드를 활성화하여 더 많은 XHR/fetch/WebSocket를 트리거합니다.
4. **빈 데이터, 빈 테이블, 자리 표시자 UI가 모두 예상됩니다** - 이에 대한 실제 로그인이나 취약점 테스트를 진행하지 마세요.

**한 문장**: mock를 사용하여 프런트 엔드 라우팅 및 구성 요소 장착을 지원하고 **outbound 요청만 기록**하세요. 백엔드가 무엇을 반환하는지는 중요하지 않습니다. 중요한 것은 프런트엔드가 **전송**할 인터페이스입니다.

### 강제 금지 처리

| 금지하다 | 대안 |
|---|---|
| Phase 1 완료 전 grep/curl/Read 메인 entry `index-*.js` 추출 API path | `OUTDIR/harvest_static.py` 실행 |
| 손으로 쓴 `extract_apis.py` 및 harvest를 대체하는 기타 스크립트 | `OUTDIR/harvest_static.py` 변경 후 재실행 |
| 동일한 grep/ 명령이 2회 이상 실패했지만 여전히 반복됩니다. | 전략 변경: tool_logs 읽기, harvest 변경, reference 확인 |
| 액세스 제어 A/B를 건너뛰고 `scripts/` 원본 버전을 직접 실행하세요. | OUTDIR에 복사하고 대상에 따라 변경 |
| 실제 사용자 이름/비밀번호, OTP, OAuth 등 인증 | stub/mock(위 참조) |
| "실제 데이터 얻기"를 이유로 stub를 건너뛰고 재정의/주입 테스트를 수행합니다. | recon의 경계에 속하는 outbound만 기록합니다. |
| 삭제, 민감한 데이터 내보내기, 일괄 쓰기 등 되돌릴 수 없는 작업 | coverage 동일한 버튼을 클릭하세요 |
| 모든 페이지와 인터페이스를 획득했다고 주장하는 미완성 runtime + 동적 열거 | "완료의 정의"를 참조하거나 제한사항을 참고하세요. |
| 모든 매개변수를 마스터했다고 주장하는 미완성 매개변수 트리거 매트릭스 + diff | Phase 3b 매트릭스 + Phase 5 diff |
| 단일 runtime 샘플로부터의 추론 필수/선택 사항 | 다중 샘플 diff 또는 검증 규칙/오류 반전 |

---

## 2층 모델 + 런닝 모드

| 층 | 산출 | 상한 |
|---|---|---|
| **정적**(JS bundle) | 전체 endpoint 경로, 라우팅 초안, 그룹 패킷 포인트 필드 후보 | HTTP 방법 없음; 매개변수는 Phase 1b여야 합니다. 런타임 접합 URL가 누락되었습니다. |
| **런타임**(라이브 세션) | 방법 + body + 응답 + 동적 URL + WS/SSE; 다중 샘플 diff 완료 매개변수 | 요청이 전송되기 전에 페이지가 실제로 렌더링되어야 합니다. 단일 샘플로는 필수/선택 사항을 결정하기에 충분하지 않습니다. |

| 작동 모드 | 엔진 | 해당되는 |
|---|---|---|
| **깊이** | `runtime_harvest.js`(인형사) | API 목록, METHOD/params/ 응답 본문, WS/SSE, 재현 가능한 일괄 실행 |
| **적용 범위** | 브라우저 + `preload.js` | 기능 포인트에 대한 더 자세한 내용을 보려면 Tab/ 팝업 창/양식을 클릭하세요. |
| **둘 다** | 먼저 depth 다음으로 coverage | 가장 완벽하고 가장 오랜 시간이 소요되는 |

**매개변수 방법**(범용 스크립트 없음): path는 harvest/ 규칙성을 사용합니다. 매개변수는 **앵커 포인트 창 확장 + UI 바인딩 체인 + 다중 샘플 diff + 오류 추론**(grep 레시피, [reference.md](reference.md) 섹션 J 참조)을 사용합니다.

---

## 완전한 정의

모든 것이 만족된 경우에만 recon가 완료되었다고 주장할 수 있습니다.

- [ ] **공전**：Phase 1 harvest 산출 `api_static.txt`、`routes.txt`、`js/`
- [ ] **런타임**: depth 또는 coverage 중 하나 이상; coverage/both는 **Hook여야 적용됩니다 + 동적 열거 링**
- [ ] **셸 입력**: path 서비스에 액세스할 때 `/login`가 아닙니다(hash 경로 참고).
- [ ] **매개변수**: coverage/both 전체 매개변수 트리거 매트릭스 + `param_samples.json`; Phase 5 병합 `params_merged.json`
- [ ] **깊이**(모듈 페이지가 비어 있는 경우): Phase 4 권한 트리를 복원하고 **module 레벨 API**가 나타날 때까지 다시 실행합니다(locale/bootstrap뿐만 아니라).
- [ ] **배송**: Phase 5 전체 출력(Phase 5 출력 표 참조); `insert_assets`는 서비스 및 엔드포인트 자산을 작성합니다.

---

## 스크립트 및 액세스 제어

`scripts/`는 참조 템플릿일 뿐입니다. 원본 버전을 직접 실행하여 최종 결과로 사용하는 것은 금지되어 있습니다.

**규칙**: 먼저 읽기 → 대상에 따라 변경 → `OUTDIR` 쓰기(예: `recon/`) → `CHANGES.md` 기억; 일치하지 않으면 방법론에 따라 다시 작성하고 구조만 빌려옵니다.

| 접근 제어 | 언제 | 참조 스크립트 → OUTDIR 복사 | 일반적으로 필요한 변경 사항 |
|---|---|---|---|
| **A(정적)** | Phase 0 이후, **먼저** 이전에 harvest/spider 실행 | `harvest_static.py` / `spider_mpa.py` | **대부분의 사이트는 기본값 regex 직접 실행 가능**；오직 manifest/방언이 일치하지 않을 때 변경 endpoint 정기적인、webpack/Vite `publicPath`、MPA exclude/cookie |
| **B(런타임)** | Phase 2 후면, depth/coverage 전면 실행 | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage 키, neutralize 성공 값, stubs, login 일반, api 접두사, hash/history |

**SPA 강제 순서**(교환 불가능, Phase 번호가 "먼저 탐색한 후 스크립트"보다 우선 적용됨):

| 단계 | ~ 해야 하다 | 금지하다 |
|---|---|---|
| Phase 0 완료 후 | 다음 기사 Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read 마스터 entry `index-*.js`(일반적으로 >500KB) |
| 액세스 제어 A | 스크립트 복사 → 필요에 따라 약간 변경 → **즉시 실행** | 먼저 API를 수동으로 추출한 다음 harvest를 추출할지 결정합니다. |
| 완료 전 Phase 1 | `wc -l` 검증 출력; 404 harvest로 변경하고 다시 시도하세요. | 필기 extract 스크립트; 다운로드되지 않은 URL에 대해 grep를 반복합니다. |
| Phase 1b 이상 | grep 전용 `OUTDIR/js/*.js` | harvest를 메인 bundle로 교체 |

- ✅ `harvest_static.py` 복사 → (선택) regex 변경 → **지금 실행**
- ❌ curl 메인 bundle → grep 여러 번 → 임시 extract 쓰기 → 마지막으로 harvest
- **MPA**：Phase 0 다음 항목 Bash = `python3 OUTDIR/spider_mpa.py ...`

---

## 도구 및 출력 제약

| 강제 | 설명하다 |
|---|---|
| 대용량 파일 | >100KB `index-*.js` **금지됨** Read/grep를 컨텍스트에 포함; OUTDIR 스크립트를 사용한 배치 |
| grep 출력 | 반드시 `\| head -20` 또는 `-m 5`; path 대화 요약만 유지하고 bundle 조각은 게시하지 마세요. |
| 확인하다 | `wc -l`, `ls\ 사용| wc -l`; Read 전체 디렉토리가 아닙니다. |
| regex 예비 탐사 | 선택 사항, 1회 이하, 50KB 이하의 소형 chunk 또는 HTML; 공식 정적 버전은 harvest입니다. |
| 참조 | 레시피/템플릿/문제 해결은 [reference.md](reference.md)를 참조하세요. inline의 전체 텍스트를 반복하지 마세요. |

---

## 실행 로드맵

```
Phase 0 카테고리 + OUTDIR
  → 출입통제 A → Phase 1 harvest (★즉시 실행 ★)
  → Phase 1b 매개변수 역방향
  → Phase 2 인증 3도어 → config.json
  → 액세스 제어 B → Phase 3 런타임 + 매개변수 매트릭스
  → Phase 4 권한 트리(필요한 경우) → Phase 3 다시 실행
  → Phase 5 병합 보고서 + insert_assets 일괄 삽입은 검색된 모든 서비스 및 엔드포인트 API 자산을 삽입합니다. 어떠한 경우에도 삽입 시 누락된 자산은 허용되지 않습니다.
```

순서대로 확인하세요. **이전 항목이 완료될 때까지 다음 항목을 입력하지 마세요. Phase**.

1. [ ] **Phase 0**: SPA/MPA의 예비 탐사; `OUTDIR` 생성 → [Phase 0](#phase-0--분류)
2. [ ] **접근 제어 A + Phase 1**: 스크립트 복사 → **즉시** harvest → `wc -l` 확인 → [Phase 1](#phase-1--공전)
3. [ ] **Phase 1b**: 앵커 포인트 창 확장 + 바인딩 레이어 → `param_candidates.json` → [Phase 1b](#phase-1b--매개변수 반전)
4. [ ] **Phase 2** : 인증용 문 3개 → `config.json` → [Phase 2](#phase-2--인증의 세 가지 문)
5. [ ] **액세스 제어 B**: runtime 스크립트 조정 → [Phase 3](#phase-3--실행 시간)
6. [ ] **Phase 3**: depth / coverage / both; 쉘 진입을 확인합니다. 매개변수 트리거 매트릭스 → `param_samples.json`
7. [ ] **Phase 4** (필요한 경우): 권한 트리 → patch stubs → Phase 3 다시 실행 → [Phase 4](#phase-4--권한 트리 복원)
8. [ ] **Phase 5**: 복합 출력 + 보고서 + `insert_assets` → [Phase 5](#phase-5--통합 및 보고)

---

## Phase 0 — 분류

HTML 항목을 당겨서 **`OUTDIR`를 만듭니다**(skill에서 `scripts/`를 변경하지 마세요):

- **SPA**: 빈 쉘 + `<div id=app>` + chunk → Phase 1–5
- **MPA**: SSR + `<form>`, 없음 endpoint bundle → 액세스 제어 후 A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

`forms.txt`, `links.txt`, `api_inline.txt`를 출력합니다. SPA forms ≒ 0인 경우 → Phase 1을 잘라냅니다.

---

## Phase 1 — 정적

[스크립트 및 액세스 제어](#스크립트 및 액세스 제어) · [도구 및 출력 제약 조건](#도구 및 출력 제약)을 준수합니다.

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: 분석 HTML script → webpack/Vite manifest → 모든 lazy chunk 다운로드 → 출력 `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk 번호 vs manifest: 404 변경 필요 harvest 다시 시도하세요. 수동으로 하지 마세요. curl 하나씩 chunk
- `api_static.txt`가 너무 적습니다 → endpoint 내에서 OUTDIR를 완화하고 정규화 후 다시 실행합니다(reference 참조).

### Phase 1b — 매개변수 역방향

path는 Phase 1에서 나옵니다. 매개변수 필드는 별도의 recon여야 합니다. grep 규칙은 [도구 및 출력 제약 조건](#도구 및 출력 제약)을 참조하세요.

**완료 기준**: 필드 이름, 전송 위치, 유형 추론, 필수 여부, 샘플 값, 신뢰 수준 등 중요한 인터페이스에 답변할 수 있습니다.

#### 1b.0 — 전송 형식

| 형태 | 매개변수는 어디에 있나요? | 정적 우선 |
|---|---|---|
| REST JSON | 본문 + 쿼리 | path 앵커 포인트 옆 `(params\|데이터\|본문)\s*:\s*\{` |
| GraphQL | `variables` | gql 템플릿, `$page: Int` |
| 기존 form | Urlencoded | `<form>`、`FormData` |
| 파일 업로드 | 다중 부분 | `FormData.append` |
| 경로 매개변수 | `/user/:id` | 라우팅 테이블 + `useParams` / `$route.params` |
| 암호화/서명 | 포함된 `sign`/`data` | Hook 암호화 기능 입력 매개변수(섹션 reference D) |

출력: 각 인터페이스는 'transport: query'로 표시됩니다.|JSON|형태|그래프|암호화`。

#### 1b.1 — 앵커 창 확장

알려진 path를 앵커로 사용하여 창을 확장하여 그룹 패키지 개체를 찾습니다.

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| 포장층 | 매개변수 단서 |
|---|---|
| axios 인스턴스 | `data` / `params` |
| 통합 request | 인터셉터는 전역 필드를 주입합니다. |
| OpenAPI 클라이언트 | method 서명 생성 |
| 반응 쿼리 / SWR | hook 두 번째 매개변수 |
| 구성 가능한 뷰 | composable 입력 매개변수 |

유형 잔여물: `yup`/`zod`/rules, `Form.Item name=`, 임베디드 Swagger.

→ `param_candidates.json`：`{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — 바인딩 레이어

```
양식 필드 → onFinish/handleSubmit → 변환 → API 페이로드
```

| 바인딩 소스 | 기술 |
|---|---|
| 양식 submit | submit → transform → API를 따르세요. |
| 테이블 검색 | `getFieldsValue()` → `params` |
| 라우팅 | `:id` / `?tab=` |
| 인터셉터 | 글로벌 `tenantId`, 페이징, sign |
| 열거형 select | `options` → API 열거값 |

DevTools call stack `fetch`/`XHR.send`의 패키지 기능을 따릅니다.

#### 1b.3 — 패키지 그룹화에 대한 세 가지 질문(≠ Phase 2 인증 도어 3개)

| 묻다 | 무엇에 대답해야 할까요? |
|---|---|
| **집회** | payload build, transform의 흔적은 어디에 있습니까? |
| **확인하다** | 필수, 패턴, 열거형 |
| **전염** | path / query / body / multipart / 헤드 |

인터셉터 게이트(Phase 2)는 우연히 전역 주입 필드(Authorization, `X-Tenant-Id`, sign)를 읽습니다.

#### 1b.4 - Phase 3과의 인터페이스

후보 필드는 정적/바인딩 레이어에서 나옵니다. **필수/선택/조건부 종속성**은 Phase 3 매개변수 매트릭스 + diff + Phase 5 오류 반전이 필요합니다.

---

## Phase 2 — 인증의 세 가지 문

`OUTDIR/js/` grep(`head` 포함)에서 `config.json`를 작성합니다(레시피는 reference 참조).

| 문 | 질문 | 키워드 |
|---|---|---|
| **렌더링 게이트** | 로그인되었는지 어떻게 알 수 있나요? | `isLogin`、`getToken`、쿠키/로컬 스토리지 |
| **인터셉터 도어** | 점프 `/login`를 유발하는 요인은 무엇입니까? | `response_code`, `errno`, 액시오스 인터셉터 |
| **컨텐츠 게이트** | 메뉴/권한은 어디서 나오나요? | `menu`、`permission`、`role`、`acl`、`routes` |

localStorage 키 이름을 자격 증명으로 사용하는 것은 금지되어 있습니다. chunk/ 요청 체인에서 확인되어야 합니다.

**종료 = 접근 제어 B**: 결론은 `config.json`로 떨어지고, `OUTDIR/runtime_harvest.js` / `preload.js`로 변경되었습니다.

### Phase 2b — API 관찰(옵션)

OUTDIR 내에서 `preload.js`를 사용하여 세션 키 이름 Authorization 및 중첩된 API URL를 확인합니다.

| 설정 | 산출 |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | headers 관찰 |
| `extractUrlsFromResponse: true` | 내면의 아이에게 응답하세요 API |
| `observe.storageReads/cookieReads: true` | 백필 config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

coverage는 `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__` 각 라운드에서 내보내집니다.

---

## Phase 3 — 런타임

게이트 B를 통과해야 합니다. [경계 및 금지 사항](#국경과 금지agent-꼭 읽어야 할--위반이란 선을 넘는다는 뜻이다.) · 자격 증명 없음 mock 정책을 준수합니다.

`config.json` 설정 `"runtimeMode": "depth" | "적용 범위" | "both"`(템플릿은 reference 참조).

### Hook 및 stub(depth + coverage에서 공유)

| 층 | 범위 | 목적 |
|---|---|---|
| L1 정확함 | auth/ 권한/bootstrap stub | 첫 화면 인증 통과 |
| L2 네거티브 보정 | 모든 JSON 응답 | 로그인 코드 없음 → 성공 |
| L3 안전하게 보관하세요 | L1를 놓치는 `/api` 등 | 성공한 몸체를 비우고 UI를 엽니다. |

- **depth**: fake auth + `forward` 비즈니스 코드 변경 + `stubs`; `routes`(hash/history)를 횡단합니다. `runtime_api.json` 출력
- **coverage**: **document-start** `preload.js`(CDP `addScriptToEvaluateOnNewDocument` 또는 Userscript) 주입

확인: `window.__API_RECON_PRELOAD__`가 존재합니다. 비즈니스 path가 `/login`를 반환하지 않습니다.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage 동적 열거(필수)

1. 기본 탐색/사이드바 - 클릭할 때마다 네트워크가 나올 때까지 1~3초 기다립니다.
2. 탭 — `role=tab`、`.ant-tabs-tab`
3. 표 - 첫 번째 행 보기/수정/세부정보
4. 도구 모음 - 내보내기, 필터, 새로 만들기(**되돌릴 수 없는 삭제 방지**)
5. 각 모듈 - API/ 라우팅 병합
6. SPA — `routes.txt`에 포함되지 않음 path 제어 `pushState`(MPA 비활성화됨)

**매개변수 트리거 매트릭스**(필수): 각 모듈은 작업 유형 **diff 다중 샘플**에 따라 한 번 기록됩니다.

| 작동하다 | 일반적으로 추가 매개변수 |
|---|---|
| 목록 첫 화면 | 페이지 매김 + 기본 필터 |
| 검색을 클릭하세요 | 키워드、필터 |
| 고급 필터링 | 더 많은 것 optional |
| 신규/수정 | entity를 완료하세요 |
| 일괄/내보내기/정렬 | `ids[]`、`exportType`、`sortField` |

**outbound body/headers 아래의 stub는 여전히 유효합니다** - 요청 시 적용됩니다. 녹화 → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + 문서 시작 사전 로드
- **React**: `routes.txt` + 사이드바 클릭 + `pushState`
- **both**：첫 번째 3a depth，다시 3b coverage

---

## Phase 4 — 권한 트리 복원

**트리거**: 모듈 페이지가 비어 있습니다. / 각 경로에는 bootstrap만 있습니다(예: locale) → 콘텐츠 게이트를 통과하지 못했습니다.

| 현상 | 의미 |
|---|---|
| 성공적으로 쉘을 셸 | 렌더 게이트 + 인터셉터 게이트 통과 |
| 사이드바 항목 누락/빈칸 클릭 | stub shape 또는 불완전한 권한 코드 |
| 각 경로 API는 동일하며 매우 적습니다. | `v-if permission` 실패 |
| `routes.txt`는 bundle보다 훨씬 작습니다. | auth 모듈에서 완료해야 합니다. |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

일반적인 체인: `role_permissions`(flat codes) + `permissions/all`(tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

중간 출력: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

stub 확인: 외부 `response_code`가 요격체 도어와 정렬되어 있음; flat codes는 tree와 정렬됩니다. `routes`는 `route_map` 전체 link를 오버레이합니다.

**`config.json`를 업데이트한 후 Phase 3**을 다시 실행하세요. 대형 SPA 조정 가능한 `waitUntil`, `routeTimeout`, `perRouteMs`(reference A3/I 섹션 참조).

---

## Phase 5 — 통합 및 보고

### 출력 테이블

| 문서 | 단계 | 콘텐츠 |
|---|---|---|
| `js/`、`api_static.txt`、`routes.txt`、`chunkmap.txt` | 1 | 정적 bundle 및 path |
| `param_candidates.json` | 1b | 정적 매개변수 필드 후보 |
| `config.json` | 2 | 3도어 + runtime 구성 |
| `runtime_api.json` | 3a | depth 상세 녹화(WS/SSE 포함) |
| `param_samples.json`、`scan_raw.json`、`api_detail.json` | 3b | 다중 샘플, 클릭 로그, detail |
| `route_map.json` 등 | 4 | 권한 트리 중간 파일(실행된 경우) |
| `params_merged.json` | 5 | 매개변수 필드 + 신뢰도 병합 |
| `api_merged.txt` | 5 | `방법 /path [매개변수] [정적\|실행 시간\|둘 다]` |
| `site_map.json` | 5 | 라우팅, API, params, 기능 포인트, 제한 사항 |
| **insert_assets** | 5 | 모든 서비스 및 엔드포인트 자산을 자산 라이브러리에 작성 |

### 5b — 매개변수 병합

`param_samples.json`에서 diff까지, **범용 병합 스크립트 없음**. 신뢰도 규칙(높음/중간/낮음/보류 중인 트리거)은 reference J7를 참조하세요.

### 5c — 백캐스팅 오류

불완전한 요청은 승인 범위 내에서 전송될 수 있습니다. 400 읽기(** 매개변수 recon, 비취약성 테스트**): `field 'x' is required`, 열거 오류 등 암호화 전 `data` 패키징, `variables` 및 `bizData`를 참고하세요.

보고서에는 runtimeMode, 정적/런타임 API 수, 매개변수 신뢰도, 발견되지 않은 모듈, 참조 스크립트와 관련된 `CHANGES.md` 요약이 표시되어야 합니다.

`site_map.json` 제안된 구조:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

더 많은 필드와 grep 레시피는 [reference.md](reference.md)를 참조하세요.

---

## 일반 지침

- **프레임워크 독립적**: webpack/Vite/Angular lazy load 동일한 방법
- **전송**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web가 범위 내에 있지 않습니다.
- **SSR**: 클라이언트 fetch를 녹화할 수 있습니다. RSC/Server Actions는 완전히 열거 가능하지 않습니다.
- **블라인드 존**: JSVMP, WASM, HMAC/mTLS 강력한 검증 → 정적 + 주석 제한
- **매개변수 사각지대**: 조건부 연결, hidden params, WASM 패키지 → "트리거 대상"/"도달할 수 없음"
- **Static은 안전망입니다**: runtime Static은 차단된 경우에도 여전히 endpoint를 열거할 수 있습니다.

---

## 추가 리소스

- Grep 레시피, `config.json` 템플릿, 문제 해결, Hook, 매개변수 역방향 J 섹션, site_map 템플릿: **[reference.md](reference.md)**
- 참조 스크립트 경로는 [스크립트 및 액세스 제어](#스크립트 및 액세스 제어) 표를 참조하세요.
