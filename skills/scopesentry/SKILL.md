---

## name: scopesentry-mcp
description: ScopeSentry MCP를 통해 보안 스캐닝 플랫폼(프로젝트, 작업, 템플릿, 자산, 노드)을 관리합니다. 사용자가 ScopeSentry, MCP, API Key, 스캔 작업 및 자산 쿼리를 언급할 때 사용됩니다.

# ScopeSentry MCP 사용자 안내서

ScopeSentry 인스턴스를 배포한 사용자의 경우. Cursor(또는 다른 MCP 클라이언트)를 통해 플랫폼에 연결하면 로컬 소스 코드가 필요하지 않습니다.

## 1. 준비

### 1.1 서비스에 액세스할 수 있는지 확인

- 기본 웹 화면: `http://<호스트>`
- MCP 엔드포인트: `http://<호스트>/mcp`(리버스 프록시를 사용하는 경우 실제 `/mcp` 주소 입력)

### 1.2 API Key 생성

1. 브라우저 로그인 ScopeSentry Web 인터페이스
2. **API Key** 관리페이지에 접속하여 키 생성(또는 관리자가 제공하는 인터페이스를 통해 생성)
3. 반환된 `ssk_...` 문자열을 저장합니다(**한 번만 표시됨**)

### 1.3 구성 Cursor MCP

Cursor → Settings → MCP → 서버 추가:

```json
{
  "mcpServers": {
    "scopesentry": {
      "url": "http://<당신의 호스트>:8082/mcp",
      "headers": {
        "X-API-Key": "ssk_yourkey"
      }
    }
  }
}
```

또한 사용 가능: `Authorization: Bearer ssk_당신의 열쇠`

구성이 완료되면 MCP를 다시 시작하거나 Cursor를 다시 로드하고 `list_projects`, `list_assets` 등이 도구 목록에 나타나는지 확인합니다.

---

## 2. 도구 개요


| 도구                     | 사용                |
| ---------------------- | ----------------- |
| `list_projects`        | 태그별로 그룹화된 프로젝트 트리(프로젝트 ID 포함) |
| `list_projects_data`   | 이름으로 검색 가능한 페이지가 매겨진 프로젝트 목록     |
| `get_project`          | 프로젝트 세부정보              |
| `create_project`       | 새 프로젝트              |
| `list_tasks`           | 스캔 작업 목록            |
| `get_task`             | 작업 상세              |
| `list_scan_templates`  | 스캔 템플릿 목록            |
| `get_scan_template`    | 템플릿 세부정보              |
| `list_plugin_modules`  | 파이프라인 모듈 이름 스캔          |
| `list_plugins`         | 사용 가능한 플러그인(hash, 기본 매개변수 포함) |
| `create_scan_template` | 스캔 템플릿 만들기            |
| `create_scan_task`     | 검사 작업 만들기            |
| `list_assets`          | 다양한 유형의 자산 쿼리(페이지가 매겨진 목록)       |
| `count_assets`         | 통계자산수(`/api/assets/common/total`) |
| `get_asset_detail`     | 자산 또는 취약점 세부정보           |
| `add_asset_tag`        | 자산에 태그 추가           |
| `list_nodes`           | 스캔 노드 목록            |


각 공구 매개변수에는 MCP 공구 설명(schema)이 적용됩니다. `list_assets` / `count_assets`의 search 및 filter는 동일한 구문을 갖습니다. 자산을 쿼리하기 전에 `list_assets` description를 읽을 수 있습니다.

"총 항목 수"를 알아야 할 경우 `count_assets`(Web 페이징 총 인터페이스에 해당)를 사용하십시오. 총 개수를 계산하기 위해 `list_assets` 페이지를 반복해서 넘길 필요가 없습니다.

---

## 3. 일반적인 워크플로

### 3.1 프로젝트별 자산 확인

사용자 또는 컨텍스트에 **프로젝트 조건**이 있는 경우 먼저 `filter.project`를 가져와 범위를 좁혀 프로젝트 간 데이터가 너무 많아 응답 속도가 느려지는 것을 방지하세요. 명확한 프로젝트가 없는 경우 프로젝트 심사를 필수로 추가할 필요는 없습니다.

1. `list_projects` 또는 `list_projects_data` 대상 프로젝트의 **ObjectID**(`id` / `children[].value`)를 가져옵니다.
2. `list_assets` `filter.project` 입력 (**ID여야 하며, 프로젝트 한글명은 기재 불가**)

```json
{
  "asset_type": "asset",
  "pageIndex": 1,
  "pageSize": 20,
  "search": "domain=^example.com",
  "filter": {
    "project": ["<프로젝트객체ID>"]
  }
}
```

### 3.2 검사 작업 생성

1. `list_nodes` 온라인 노드 이름 가져오기
2. `list_scan_templates` 또는 `create_scan_template` 템플릿 가져오기 **ObjectID**
3. `create_scan_task`: `name`, `node`는 필수이며, `template`는 템플릿 ID를 입력합니다(템플릿 이름은 입력할 수 없습니다).

**대상 소스 `targetSource`(Web 끝과 일치): **

| 타겟소스 | 설명하다 | 필수 매개변수 |
| --- | --- | --- |
| `general` | 대상을 직접 입력하세요. | `target` |
| `project` | 프로젝트에서 대상 읽기 | `project`(항목 ObjectID 어레이) |
| `asset` | Web 자산 라이브러리에서 검색 | `search`; 옵션 `project`, `filter`, `targetNumber` |
| `RootDomain` | 루트 도메인 이름 데이터베이스에서 검색 | `search`; 옵션 `project`, `filter`, `targetNumber` |
| `subdomain` | 하위 도메인 데이터베이스에서 검색 | `search`; 옵션 `project`, `filter`, `targetNumber` |
| `UrlScan` | URL 스캔 결과에서 검색 | `search`; 옵션 `project`, `filter`, `targetNumber` |
| `*Source`(예: `subdomainSource`) | 자산 페이지 "선택/검색"에서 생성됨 | `targetTp=search`인 경우에는 `search`를 사용하십시오. `targetTp=select`일 때 `targetIds`를 사용하세요 |

**예: 루트 도메인 이름을 직접 스캔합니다.**

```json
{
  "name": "example- 하위 도메인 이름 수집",
  "node": ["node-1"],
  "template": "<템플릿 개체 ID>",
  "targetSource": "general",
  "target": "example.com\nfoo.com",
  "project": ["<프로젝트객체ID>"]
}
```

**예 - 하위 도메인 이름 데이터베이스에서 계속 검색(이전 작업 이름으로 필터링):**

```json
{
  "name": "example- 포트 및 취약점",
  "node": ["node-1"],
  "template": "<후속 모듈 템플릿 ObjectID>",
  "targetSource": "subdomain",
  "search": "task==\"example- 하위 도메인 이름 수집\"",
  "project": ["<프로젝트객체ID>"]
}
```

### 3.3 루트 도메인 이름의 완전한 정보 수집(2단계 권장)

입력이 **루트 도메인 이름**이고 **전체 정보 수집**을 수행하려는 경우 전체 파이프라인을 한 번에 실행하는 대신 두 번에 걸쳐 스캔하는 것이 좋습니다.

**원인:** 분산 작업이 **단일 대상** 단위로 분산됩니다. 루트 도메인 이름을 대상으로 사용하는 경우 노드에 루트 도메인 이름이 할당된 후 노드에서 휩쓸린 하위 도메인 이름이 해당 노드에서 후속 모듈을 계속 실행하므로 부하가 고르지 않고 속도가 느려지며 오류가 발생하기 쉽습니다.

**모범 사례:**

1. **1단계 - 하위 도메인만 수집**
   - `targetSource`: `general`
   - `target`: 모든 루트 도메인 이름(여러 줄)
   - 템플릿: `SubdomainScan`, `SubdomainSecurity`(하위 도메인 이름 스캐닝 + 하위 도메인 이름 인수)만 활성화합니다.
   - `get_task`를 사용하여 작업이 완료될 때까지 기다립니다.

2. **2단계 – 후속 모듈**
   - `targetSource`: `subdomain`
   - `search`: `task=="<1단계 작업 이름>"` (작업 이름과 정확히 일치)
   - 옵션 `project` 좁은 범위
   - 템플릿: 포트 스캐닝, 자산 매핑, 취약점 스캐닝 등(SubdomainScan는 포함되지 않을 수 있음)
   - 하위 도메인 이름은 각 노드에 독립적인 대상으로 배포되며 병렬 효율성이 더 높습니다.

동일한 효과로 Web 인터페이스의 "하위 도메인 이름" 자산 페이지에서 작업 이름으로 필터링한 후 "하위 도메인 이름에서 작업 생성"을 사용할 수도 있습니다.

```mermaid
flowchart LR
  A[루트 도메인 이름 목록] --> B[단계1: general + SubdomainScan]
  B --> C[하위 도메인 이름 저장]
  C --> D[단계2: subdomain + task==단계1작업 이름]
  D --> E[포트/자산/취약점 및 기타 모듈]
```

### 3.4 스캐닝 템플릿 만들기

1. `list_plugin_modules` → 모듈 이름 목록
2. `list_plugins`(`module`로 필터링 가능) → 각 플러그인 `hash` 및 기본 `parameter`
3. `create_scan_template`: `modules`를 사용하여 "모듈 → 플러그인 hash 어레이"를 지정합니다.

---

## 4. 자산 조회(`list_assets` / `count_assets`)

`count_assets` 및 `list_assets`는 동일한 `asset_type`, `search` 및 `filter`를 사용하고 Web 터미널 `/api/assets/common/total`에 해당하는 `{ "total": N }`를 반환합니다.

```json
{
  "asset_type": "subdomain",
  "search": "task==\"특정 작업 이름\"",
  "filter": {"project": ["<프로젝트객체ID>"]}
}
```

**성능 권장 사항(`list_assets`/`count_assets` 공통):** 프로젝트 조건이 있는 경우 범위를 좁히려면 `filter.project`가 선호됩니다. `search`에서는 응답 속도를 늦추는 대규모 `=` 퍼지 쿼리를 피하기 위해 설정된 인덱스 필드([4.3](#43-search-검색 표현) 참조)에 일치하는 `==` 또는 `^` 접두사 일치를 사용해 보십시오. 프로젝트 컨텍스트가 없으면 프로젝트 필터링이 강제로 적용되지 않습니다.

`filter.project`를 지원하는 타입은 표 [4.4](#44-filter-정밀한 필터링)와 같습니다.

### 4.1 자산 유형 `asset_type`

`asset`、`RootDomain`、`subdomain`、`app`、`mp`、`UrlScan`、`SensitiveResult`、`DirScanResult`、`crawler`、`vulnerability`、`PageMonitoring`、`IPAsset`、`SubdomainTakerResult`

별칭의 예: `web`→asset, `vuln`→vulnerability, `ip`→IPAsset, `url`→UrlScan

### 4.2 매개변수 설명


| 매개변수                       | 설명하다                                      |
| ------------------------ | --------------------------------------- |
| `pageIndex` / `pageSize` | 페이지 매김, 기본값 1/20                            |
| `search`                 | 검색 표현식(다음 섹션 참조)                              |
| `filter`                 | 정밀 필터링 JSON(다음 섹션 참조)                          |
| `sort`                   | UrlScan 및 DirScanResult만 `length`별 정렬을 지원합니다. |
| `sid`                    | SensitiveResult만 해당: 민감한 규칙 이름                |


`search` 및 `filter` **동시에 사용할 수 있습니다**.

### 4.3 search 검색식

맞춤형 DSL(**SQL 아님**):


| 연산자  | 의미   | 색인 | 예                          |
| ---- | ---- | ---- | --------------------------- |
| `=`  | 퍼지 매칭(regex) | 인덱싱 없음 | `domain=example`            |
| `==` | 정확히 일치(합동) | **색인으로 이동** | `port==443`                 |
| `!=` | 들어오지 못하게 하다   | — | `port!="80"`                |
| `&&` | 그리고    | — | `domain==example.com && port==443` |
| `||` | 또는    | — | `제목=관리자 || 본문=로그인` |


**인덱스 및 연산자:** `domain`, `ip`, `port`, `title` 및 기타 필드는 인덱싱되지만 **`==` 합동** 또는 **`^` 접두사 일치로 시작하는 값**(예: `domain=^example.com`)만 인덱싱할 수 있습니다. **`=`는 regex 퍼지 매칭으로 변환되어 인덱스를 사용할 수 없으며, 데이터 양이 많을 경우 속도가 느려질 수 있습니다.

**모든 유형에 공통되는 search 필드:** `tag`, `task`(작업 이름), `rootDomain`

**project는 search에 쓸 수 없습니다**(`&&`와 결합하면 유효하지 않거나 오류가 보고됩니다). 체 품목에는 `filter.project`를 사용하십시오.

**다양한 유형에 일반적으로 사용되는 search 필드:**


| asset_type           | 필드                                                                                  |
| -------------------- | ----------------------------------------------------------------------------------- |
| 유산                | 도메인, IP, 포트, 서비스, 앱, 제목, 상태 코드, 아이콘, 배너, 유형, 본문, 헤더 |
| 루트도메인           | 도메인, ICP, 회사                                                                |
| 하위 도메인            | 도메인, IP, 유형, 값                                                             |
| 앱                  | 이름, ICP, 회사, 카테고리, 설명, URL, APK                                 |
| mp                   | 이름, ICP, 회사, 카테고리, 설명, URL                                      |
| UrlScan              | URL, 입력, 소스, 결과 ID, 유형                                                  |
| 민감한 결과      | URL, sname, 본문, 정보, md5                                                         |
| DirScan결과        | URL, 상태 코드, 리디렉션, 길이                                                   |
| 취약성        | URL, vulname, 일치, 요청, 응답, 수준                                     |
| 무한 궤도              | URL, 메소드, 본문, 결과Id                                                         |
| 페이지모니터링       | URL, 해시, 차이점, 응답                                                           |
| IP자산              | IP, 도메인, 포트, 서비스, webServer, 앱                                           |
| 하위 도메인TakerResult | 도메인, 값, 유형, 응답                                                       |


**search 예:**

- `domain==www.example.com && port==443`(합동, 인덱스)
- `domain=^example.com`(접두사 일치, 인덱싱)
- `ip==192.168.1.1`
- `task=="작업 이름"`
- `level==high`(취약성)
- `statuscode==200`(DirScanResult)

`title=admin`와 같이 퍼지 포함이 필요한 경우 `=`를 사용하십시오(인덱싱이 없으면 프로젝트 및 기타 조건에 따라 범위를 좁히는 것이 좋습니다).

### 4.4 filter 정밀 필터링

JSON 개체: key와 동일하며 여러 값은 **OR**, 다른 key는 **AND**입니다.

**`project`는 프로젝트 조건이 있는 경우 먼저 사용됩니다.** 사용자 또는 컨텍스트가 프로젝트를 지정하고 asset_type가 `project`를 지원하는 경우 범위를 좁히기 위해 가져와야 합니다. 프로젝트 정보가 없을 경우 필수사항은 아닙니다.


| 필터 키   | 의미       | 값 설명                                                     |
| ------------ | -------- | -------------------------------------------------------- |
| `project`    | 프로젝트     | **ObjectID**, `list_projects` / `list_projects_data`로 획득 |
| `task`       | 소스 태스크     | **작업 이름**, `list_tasks`의 `name`를 사용합니다.                         |
| `port`       | 포트       | `"443"`와 같은                                                |
| `service`    | 서비스/계약    | `"https"`와 같은                                              |
| `app`        | 지문 적용     | `"Nginx"`와 같은                                              |
| `icon`       | 아이콘 hash  |                                                          |
| `statuscode` | HTTP 상태 코드 | 주로 asset에 사용됩니다.                                               |
| `status`     | 상태       | UrlScan/DirScan HTTP 코드; 취약점/민감정보 처리현황                       |
| `level`      | 취약점 수준     | 중요 / 높음 / 중간 / 낮음 / 정보                    |
| `type`       | 유형       | 예를 들어 하위 도메인 이름 레코드 유형 A, CNAME                                         |
| `color`      | 민감한 규칙 색상   | 민감한 결과                                          |
| `sname`      | 민감한 규칙 이름    | 민감한 결과                                          |
| `tags`       | 상표       |                                                          |


**다양한 유형 사용 가능 filter key:**


| asset_type                            | 필터 키                                                      |
| ------------------------------------- | --------------------------------------------------------------- |
| 유산                                 | 프로젝트, 포트, 서비스, 앱, 아이콘, 상태 코드, 유형, 작업, 태그 |
| 루트도메인                            | 프로젝트, 태그                                                   |
| 하위 도메인                             | 프로젝트, 유형, 작업, 태그                                       |
| 앱/MP                              | 프로젝트, 태그                                                   |
| UrlScan                               | 상태, 태그                                                    |
| DirScan결과                         | 상태, 태그                                                    |
| 민감한 결과                       | 상태, 색상, 이름, 태그                                      |
| 무한 궤도                               | 프로젝트, 작업, 태그                                             |
| 취약성                         | 프로젝트, 레벨, 상태, 작업, 태그                              |
| PageMonitoring / SubdomainTakerResult | 태그                                                            |
| IP자산                               | 프로젝트, 포트, 서비스, 앱                                     |


**filter 예:**

```json
{"project": ["<프로젝트객체ID>"], "port": ["443"]}
```

**결합된 쿼리 예:**

```json
{
  "asset_type": "asset",
  "search": "domain=^baidu && port==443",
  "filter": {"project": ["<프로젝트객체ID>"]},
  "pageIndex": 1,
  "pageSize": 10
}
```

**알아채다:**

- 프로젝트 조건이 있는 경우 `filter.project`에 우선 순위가 부여됩니다(지원되는 경우). 프로젝트 컨텍스트가 없으면 필수가 아닙니다.
- `filter.project` 아이템 표시명을 입력하지 마세요.
- 알려진 값에는 `==`를 사용하고 접두사에는 `^`를 사용합니다. 대형 테이블에서 `=` 퍼지 매칭 남용 방지
- UrlScan의 HTTP 상태는 `filter.status`를 사용합니다. DirScanResult는 `statuscode==200`와 함께 search에서 사용할 수 있습니다.
- SensitiveResult 규칙 이름에 따르면: `search`는 `sname=규칙 이름` 또는 `filter.sname`를 사용합니다.

### 4.5 sort 정렬

**UrlScan**, **DirScanResult**만 지원:

```json
{"length": "ascending"}
```

다른 유형은 `sort`를 무시하고 기본적으로 시간순으로 정렬합니다.

---

## 5. 스캔 템플릿 모듈 이름

`TargetHandler`、`SubdomainScan`、`SubdomainSecurity`、`PortScanPreparation`、`PortScan`、`PortFingerprint`、`AssetMapping` 、`AssetHandle`、`URLScan`、`WebCrawler`、`URLSecurity`、`DirScan`、`VulnerabilityScan`、`PassiveScan`

---

## 6. 문제 해결


| 현상        | 다루다                                                 |
| --------- | -------------------------------------------------- |
| MCP 도구 없음   | URL, API Key, ScopeSentry가 실행 중인지 확인하세요.                    |
| 401 / 403 | API Key 재생성 또는 교체                                    |
| 자산을 찾을 수 없습니다.     | `filter.project`가 ObjectID인지 확인하세요. search에 project를 쓰지 마세요. |
| 템플릿/작업 생성 실패 | `template`는 ObjectID 템플릿이어야 합니다. `node`는 온라인 노드 이름을 입력합니다.            |
| 쿼리가 느리거나 중단되었습니다.   | 항목이 있으면 `filter.project`를 추가하십시오. search. 대신 인덱스 필드에 `==` 또는 `^` 접두사를 사용하고 `=`를 적게 사용하세요. `pageSize`를 줄이세요 |


---
