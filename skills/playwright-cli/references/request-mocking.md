# 요청 모킹

네트워크 요청을 가로채고, 조롱하고, 수정하고, 차단합니다.

## CLI 경로 명령

```bash
# Mock with custom status
playwright-cli route "**/*.jpg" --status=404

# Mock with JSON body
playwright-cli route "**/api/users" --body='[{"id":1,"name":"Alice"}]' --content-type=application/json

# Mock with custom headers
playwright-cli route "**/api/data" --body='{"ok":true}' --header="X-Custom: value"

# Remove headers from requests
playwright-cli route "**/*" --remove-header=cookie,authorization

# List active routes
playwright-cli route-list

# Remove a route or all routes
playwright-cli unroute "**/*.jpg"
playwright-cli unroute
```

## URL 패턴

```
**/api/users - 정확한 경로 일치
**/api/*/details - 경로의 와일드카드
**/*.{png,jpg,jpeg} - 파일 확장자 일치
**/search?q=* - 쿼리 매개변수 일치
```

## 실행 코드를 사용한 고급 모의

조건부 응답의 경우 본문 검사, 응답 수정 또는 지연을 요청합니다.

### 요청에 따른 조건부 응답

```bash
playwright-cli run-code "async page => {
  await page.route('**/api/login', route => {
    const body = route.request().postDataJSON();
    if (body.username === 'admin') {
      route.fulfill({ body: JSON.stringify({ token: 'mock-token' }) });
    } else {
      route.fulfill({ status: 401, body: JSON.stringify({ error: 'Invalid' }) });
    }
  });
}"
```

### 실제 응답 수정

```bash
playwright-cli run-code "async page => {
  await page.route('**/api/user', async route => {
    const response = await route.fetch();
    const json = await response.json();
    json.isPremium = true;
    await route.fulfill({ response, json });
  });
}"
```

### 네트워크 장애 시뮬레이션

```bash
playwright-cli run-code "async page => {
  await page.route('**/api/offline', route => route.abort('internetdisconnected'));
}"
# Options: connectionrefused, timedout, connectionreset, internetdisconnected
```

### 지연된 응답

```bash
playwright-cli run-code "async page => {
  await page.route('**/api/slow', async route => {
    await new Promise(r => setTimeout(r, 3000));
    route.fulfill({ body: JSON.stringify({ data: 'loaded' }) });
  });
}"
```
