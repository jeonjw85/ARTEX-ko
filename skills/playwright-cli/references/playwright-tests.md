# Playwright 테스트 실행

Playwright 테스트를 실행하려면 `npx playwright test` 명령이나 패키지 관리자 스크립트를 사용하세요. 대화형 HTML 보고서를 열지 않으려면 `PLAYWRIGHT_HTML_OPEN=never` 환경 변수를 사용하세요.

```bash
# Run all tests
PLAYWRIGHT_HTML_OPEN=never npx playwright test

# Run all tests through a custom npm script
PLAYWRIGHT_HTML_OPEN=never npm run special-test-command
```

# Playwright 테스트 디버깅

실패한 Playwright 테스트를 디버깅하려면 `--debug=cli` 옵션을 사용하여 실행하세요. 이 명령은 시작 시 테스트를 일시 중지하고 디버깅 지침을 인쇄합니다.

**중요**: 백그라운드에서 명령을 실행하고 "디버깅 지침"이 인쇄될 때까지 출력을 확인합니다. 완료한 후에는 명령을 중지하십시오.

세션 이름이 포함된 지침이 인쇄되면 `playwright-cli`를 사용하여 세션을 연결하고 페이지를 탐색합니다.

```bash
# Run the test
PLAYWRIGHT_HTML_OPEN=never npx playwright test --debug=cli
# ...
# ... debugging instructions for "tw-abcdef" session ...
# ...

# Attach to the test
playwright-cli attach tw-abcdef
```

탐색하고 수정 사항을 찾는 동안 백그라운드에서 테스트를 계속 실행하세요.
테스트가 시작되면 일시정지되므로 특정 위치에서 넘어가거나 일시정지해야 합니다.
문제가 있을 가능성이 가장 높은 곳.

`playwright-cli`로 수행하는 모든 작업은 해당 Playwright TypeScript 코드를 생성합니다.
이 코드는 출력에 나타나며 테스트에 직접 복사할 수 있습니다. 대부분의 경우 특정 로케이터나 기대치를 업데이트해야 하지만 앱의 버그일 수도 있습니다. 당신의 판단을 사용하십시오.

테스트를 수정한 후 백그라운드 테스트 실행을 중지합니다. 테스트가 통과했는지 확인하려면 다시 실행하세요.
