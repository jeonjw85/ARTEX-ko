"use client";

import * as React from "react";

import { CpuIcon, FlaskConicalIcon, KeyboardIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { Settings } from "@/lib/types";

import { UpdateCard } from "./_components/update-card";

export default function SystemSettingsPage() {
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 운영 제약 조건 주입 범위(둘 다 기본적으로 활성화되어 있음)
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // 실험적 기능: noa 컨텍스트 압축(기본값은 꺼짐).
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // 순수한 프런트엔드 선호: /api/settings를 사용하지 말고 localStorage를 직접 읽고 쓰십시오.
  const sendMode = useChatSendMode();

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    setGlobalProxyInput(s.global_proxy ?? "");
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
  }, []);

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("동시성 수는 0보다 큰 정수여야 합니다.");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("저장된 동시 작업 수 agent(나중에 시작된 작업에 적용)");
      })
      .catch((e) => toast.error("저장 실패:" + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const savePython = () => {
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("Python 인터프리터 구성이 저장되었습니다.");
      })
      .catch((e) => toast.error("저장 실패:" + (e as Error).message))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSaving(false));
  };

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "Agent 자동 바인딩 트래픽이 켜졌습니다." : "Agent 자동 바인딩 트래픽이 종료되었습니다.");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`저장 실패: ${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "noa 컨텍스트 압축이 활성화되었습니다(후속 실행에 적용됨)" : "noa 상황별 압축이 꺼졌습니다(내장 압축 복원).");
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(`저장 실패: ${(e as Error).message}`);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("웹 검색 구성이 저장되었습니다.");
      })
      .catch((e) => {
        toast.error("저장 실패:" + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("저장됨 Brave API Key");
      })
      .catch((e) => toast.error("저장 실패:" + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("저장됨 Tavily API Key");
      })
      .catch((e) => toast.error("저장 실패:" + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "수출 에이전트가 저장되었습니다." : "종료 프록시가 지워짐(직접 연결로 변경됨)");
      })
      .catch((e) => toast.error("저장 실패:" + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  const saveGlobalProxy = () => {
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(globalProxyInput.trim() ? "글로벌 에이전트가 저장되었습니다." : "글로벌 프록시가 삭제됨(직접 연결로 변경됨)");
      })
      .catch((e) => toast.error("저장 실패:" + (e as Error).message))
      .finally(() => setSavingGlobalProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`검색 테스트 성공 · ${r.backend}가 ${r.count} 결과를 반환합니다.`);
        else toast.error("검색 테스트 실패:" + (r.error || "알 수 없는 오류"));
      })
      .catch((e) => toast.error("검색 테스트 실패:" + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">시스템 설정</h1>
        <p className="text-muted-foreground text-sm">전역 런타임 스위치</p>
      </div>

      {/* 대신 여러 열 grid：웹 검색 카드는 나머지 카드보다 몇 배 더 크며 높이는 선택한 백엔드에 따라 다릅니다.（brave/tavily
          ~의 key 입력은 조건부 렌더링입니다.）。grid 가장 높은 것이 전체 행을 채우고 그 옆에 큰 공백이 남습니다.，
          여러 열은 콘텐츠 높이에 따라 균형 있게 자동으로 채워집니다. 카드 간격 mb 오히려 gap——다중 열 레이아웃에서
          column-gap 열 간격과 행 간격만 하위 요소 자체에 의해 설정됩니다.。 */}
      <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
        <UpdateCard />

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              트래픽 캡처
            </CardTitle>
            <CardDescription>
              전원이 켜진 후 Agent의 모든 HTTP 트래픽은 녹음 에이전트를 통해 데이터베이스에 기록되고 traffic_search / traffic_get는 Agent에 주입됩니다.
              도구 및 에이전트 구성(프롬프트 단어에 에이전트 설명이 포함됨)
              <br />
              꺼지면 트래픽이 기록되지 않습니다(기본값): Agent
              <b>습관</b>프록시 구성 및 트래픽 도구를 가져오면 프롬프트 단어도 제공됩니다.<b>포함하지 않음</b>대행사 관련 콘텐츠. 전환 후 Agent는 즉시 재구축되어 적용됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
              {trafficCapture ? "활성화됨 · 트래픽 기록 및 프록시 삽입" : "종료됨 · 로깅 없음, 프록시 삽입 없음"}
            </Label>
            <Switch
              id="traffic-capture"
              checked={trafficCapture}
              disabled={!loaded || saving}
              onCheckedChange={toggleTraffic}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Agent는 자동으로 트래픽을 바인딩합니다.
            </CardTitle>
            <CardDescription id="agent-traffic-binding-description">
              기본적으로 꺼져 있습니다. 활성화된 후, 취약점이 데이터베이스에 입력될 때 트리거되는 보고서 Agent는 기존 HTTP 요청/응답을 확인한 후 해당 트래픽을 연관시킨 후 보고서를 작성합니다.
              <b>패킷을 조회하고 추가 도구 호출을 수행하면 Token 소비가 늘어납니다.</b>
              <br />
              TCP, 패킷 캡처가 없거나 일치하는 트래픽이 여전히 정상적으로 보고될 수 있습니다. 이 스위치는 트래픽 캡처, 수동 바인딩 및 저장된 증거 보기에 영향을 주지 않습니다. 다음 라운드를 위해 Agent
              효력을 발휘하십시오. 새로운 자동 바인딩은 종료 후 즉시 거부됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
              {agentTrafficBinding ? "활성화됨 · Token 소비가 증가합니다." : "종료됨 · 수동 제본은 계속 가능"}
            </Label>
            <Switch
              id="agent-traffic-binding"
              aria-describedby="agent-traffic-binding-description"
              checked={agentTrafficBinding}
              disabled={!loaded || saving}
              onCheckedChange={toggleAgentTrafficBinding}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              글로벌 에이전트
            </CardTitle>
            <CardDescription>
              Agent 전체<b>타겟 트래픽</b>이 프록시를 통해 네트워크를 종료합니다(숨겨진 소스 IP/스프링보드 사용). 지원하다 <b>http / https / socks5</b>, 가져올 수 있습니다{" "}
              <code>user:pass</code> 인증. 비워두세요 = 직접 연결됩니다.
              <br />
              켜다<b>트래픽 캡처</b>, 기록 대리인 역할을 합니다.<b>상류</b>(전체 트래픽은 여전히 ​​데이터베이스로 삭제된 다음 이 프록시를 통해 네트워크 밖으로 나갑니다.) 캡처가 꺼지면 직접 주입됩니다.
              Agent의 bash / WebFetch는 네트워크에서 벗어났습니다. 네트워크 검색 에이전트 및 LLM 에이전트로부터 독립적입니다.
              <br />
              <b>힌트</b>:socks5 에<b>캡처 끄기</b>각 명령줄 도구를 사용하여 <code>ALL_PROXY</code> 지원 (curl
              사용 가능, 일부 도구에서는 이를 무시할 수 있음) socks5를 주로 사용하는 경우 트래픽 캡처를 활성화하는 것이 좋습니다. 이 경로는 MITM에서 사용됩니다.
              직접 전화를 걸면 도구가 눈에 띄지 않고 안정적으로 작동합니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
              프록시 주소
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="global-proxy"
                autoComplete="off"
                placeholder="socks5://user:pass@host:1080 또는 http://host:port（비워두세요=직접 연결）"
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                저장
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? "구성됨 · 모든 대상 트래픽이 이 프록시를 통해 나갑니다." : "구성되지 않음 · 대상 트래픽이 아웃바운드 네트워크에 직접 연결됨"}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              운영 제약 주입
            </CardTitle>
            <CardDescription>
              활성화한 후 각 작업의 값을 변경합니다.<b>운영상의 제약</b>(작업 개요 "작업 제약 조건"에서 유지되는 allow/deny 항목) 해당 Agent를 고정합니다.
              시스템 프롬프트는 탐색 경계를 설정하는 데 사용됩니다(예: "현재 포트만 테스트" 및 "발파 금지").
              <br />
              개별적으로 제어하여 주입 가능 <b>플래너(planner)</b>그리고 <b>실행자(worker)</b>
              ; 둘 다 기본적으로 활성화되어 있습니다. 스위치는 즉시 적용되며(다음 판독 라운드) Agent를 다시 빌드할 필요가 없습니다. Agent를 닫으면 더 이상 제약 조건이 표시되지 않습니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                인젝션 플래너(planner){injectPlanner ? " · 활성화됨" : " · 휴무"}
              </Label>
              <Switch
                id="inject-planner"
                checked={injectPlanner}
                disabled={!loaded}
                onCheckedChange={toggleInjectPlanner}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
                실행기(worker) 주입{injectWorker ? " · 활성화됨" : " · 휴무"}
              </Label>
              <Switch
                id="inject-worker"
                checked={injectWorker}
                disabled={!loaded}
                onCheckedChange={toggleInjectWorker}
              />
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <FlaskConicalIcon className="size-4" />
              실험적 기능
            </CardTitle>
            <CardDescription>
              메커니즘은 아직 검증 중이며 기본적으로 꺼져 있습니다. Agent의 동작을 변경하거나 안정성에 영향을 미칠 수 있으므로 영향을 이해한 후 활성화하십시오.
              <br />
              <b>noa 컨텍스트 압축</b>: 모델(norma v0.4.0)을 통해 긴 대화 내용을 적극적으로 압축합니다. Agent의 4가지 유형(
              <b>기획자 / 실행자 / 마스터 Agent / 대화</b>) 대신 noa를 사용하여 컨텍스트를 인계받아 내장된 압축을 대체합니다.
              압축된 원본 텍스트는 쉽게 역추적할 수 있도록 작업 작업 디렉터리에 보관됩니다. 스위치는 즉시 적용되며(후속 실행에도 적용) Agent를 다시 빌드할 필요가 없습니다.
              내장된 압축은 종료 후 즉시 복원됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
              noa 컨텍스트 압축{noaCompaction ? " · 활성화됨" : " · 휴무"}
            </Label>
            <Switch
              id="noa-compaction"
              checked={noaCompaction}
              disabled={!loaded}
              onCheckedChange={toggleNoaCompaction}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <SearchIcon className="size-4" />
              웹 검색
            </CardTitle>
            <CardDescription>
              이건 온라인에서 검색한건데<b>마스터 스위치 + 소스 구성</b>. 전원을 켠 후 다음을 수행할 수 있습니다.<b>각 Agent의 구성</b>별도로 활성화할지 선택하세요.
              <b>web_search</b>(제목/링크/초록만 반환되며 텍스트는 크롤링되지 않습니다. 크롤링은 WebFetch에서 처리됩니다.) 웹 검색<b>떠나지 않음</b>
              트래픽 캡처와 무관한 로깅 에이전트.
              <br />
              소스 선택 사항 <b>DuckDuckGo（ddgs）</b>(Key 필요하지 않음),<b>Brave(무료 버전)</b>(Brave API Key를 작성해야 함),{" "}
              <b>Tavily</b>(Tavily API Key를 작성해야 함) 또는 <b>DeepSeek</b>(현재 LLM 구성을 재사용합니다). 메인 스위치를 끄면 각
              Agent의 네트워크 검색 스위치를 사용할 수 없습니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                {webSearch ? "메인 스위치가 켜져 있습니다. · 각 Agent 구성에서 개별적으로 활성화할 수 있습니다." : "휴무 · 각 Agent 웹 검색을 활성화할 수 없습니다"}
              </Label>
              <Switch
                id="web-search"
                checked={webSearch}
                disabled={!loaded || saving}
                onCheckedChange={(v) => {
                  setWebSearch(v); // optimistic
                  saveWebSearch({ web_search_enabled: v });
                }}
              />
            </div>

            {webSearch && (
              <div className="flex items-center justify-between gap-4">
                <Label className="text-sm font-normal text-muted-foreground">소스 검색</Label>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="w-48 shrink-0">
                    <SelectValue placeholder="소스 선택" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">DuckDuckGo (ddgs · Key 없이 무료)</SelectItem>
                    <SelectItem value="brave-free">Brave(무료 버전 · Key 필요)</SelectItem>
                    <SelectItem value="tavily">Tavily(Key 필요)</SelectItem>
                    <SelectItem value="deepseek">DeepSeek (공식)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {webSearch && backend === "deepseek" && (
              <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                <p className="text-sm font-medium">DeepSeek 공식 온라인 검색</p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  이 소스는 직접 재사용됩니다.<b>현재 활성 LLM 구성</b>. 그러므로 그것은
                  <b>DeepSeek 공식 모델만 지원</b>, 그리고 이 구성은<b>anthropic 프로토콜을 사용해야 합니다.</b>
                  ——DeepSeek의 OpenAI 프로토콜 엔드포인트는 서버 측 검색을 지원하지 않습니다. LLM 구성을 전환한 후 이 소스가 유효하지 않게 될 수 있습니다.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  다른 소스와 달리 다음으로 검색하세요. <b>DeepSeek 서버 실행</b>: 각 검색은 추가 모델 호출을 소비합니다(Token 생성).
                  수수료), 검색요청<b>위 수출대리점을 거치지 않고</b>,또한<b>트래픽 추적에 포함되지 않음</b>;결과 반환<b>제목과 링크만</b>
                  (초록 없음), 본문이 필요할 때 WebFetch에 의해 크롤링됩니다.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  위의 조건이 충족되는지 확인하는 것은 귀하의 몫이며 시스템은 이를 차단하지 않습니다. 아래의 "검색 테스트" 버튼을 사용하여 실제로 실행하여 확인할 수 있습니다.
                </p>
              </div>
            )}

            {webSearch && backend === "brave-free" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                  Brave Search API Key
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">구성된</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="brave-key"
                    type="password"
                    autoComplete="off"
                    placeholder={braveKeySet ? "구성됨(변경하지 않으려면 비워 두세요)" : "Brave API Key를 입력하세요."}
                    value={braveKeyInput}
                    disabled={!loaded || savingKey}
                    onChange={(e) => setBraveKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveBraveKey}
                    disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                  >
                    저장
                  </Button>
                </div>
                {braveNeedsKey && (
                  <p className="text-xs text-amber-500">
                    Brave가 선택되었지만 Key가 구성되지 않았습니다. Key가 저장될 때까지 검색 도구가 활성화되지 않습니다.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">
                  무료 버전은 월 2,000회 정도로 제한되어 있습니다. Key를 얻으려면 https://brave.com/search/api/로 이동하세요.
                </p>
              </div>
            )}

            {webSearch && backend === "tavily" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                  Tavily Search API Key
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">구성된</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="tavily-key"
                    type="password"
                    autoComplete="off"
                    placeholder={tavilyKeySet ? "구성됨(변경하지 않으려면 비워 두세요)" : "입력 Tavily API Key (tvly-…)"}
                    value={tavilyKeyInput}
                    disabled={!loaded || savingTavilyKey}
                    onChange={(e) => setTavilyKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveTavilyKey}
                    disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                  >
                    저장
                  </Button>
                </div>
                {webSearch && backend === "tavily" && !tavilyKeySet && (
                  <p className="text-xs text-amber-500">
                    Tavily가 선택되었지만 Key가 구성되지 않았습니다. Key가 저장될 때까지 검색 도구가 활성화되지 않습니다.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">https://tavily.com로 이동하여 등록하고 API Key를 받으세요.</p>
              </div>
            )}

            {webSearch && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                  수출 대행(선택)
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder="http://host:port 또는 socks5://host:port（비워두세요=직접 연결）"
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    저장
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">
                  검색 엔드포인트(VPN/SOCKS 등)에 액세스하는 데만 사용되는 독립형 송신 프록시입니다. 트래픽을 기록하는 MITM 프록시와는 아무런 관련이 없습니다. 네트워크를 사용할 수 없을 때 이 프록시를 통해 액세스됩니다.
                </p>
              </div>
            )}

            {webSearch && (
              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <p className="text-muted-foreground text-xs">
                  현재 구성(소스 + 에이전트 + Key)을 사용하여 실제로 "test"를 검색하여 사용 가능한지 확인합니다.
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={runTest}
                  disabled={!loaded || testing}
                  className="shrink-0"
                >
                  {testing ? "테스트 중…" : "테스트 검색"}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              사용자 정의 스크립트 · Python 인터프리터
            </CardTitle>
            <CardDescription>
              사용자 정의 <b>script</b> 유형 도구는 이를 사용하여 Python를 실행합니다. 부팅 시 자동으로 감지됩니다(python3가 우선 순위를 갖습니다). 여기에 venv /를 입력할 수 있습니다.
              특정 버전에 대한 절대 경로입니다. 비워두면 런타임 시 자동으로 감지됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                className="font-mono text-sm"
                placeholder="/usr/bin/python3(비워두기 = 자동 감지)"
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                재테스트
              </Button>
              <Button onClick={savePython} disabled={!loaded || saving}>
                저장
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CpuIcon className="size-4" />
              작업 동시성 · Work Agent 번호
            </CardTitle>
            <CardDescription>
              작업당 동시에 실행할 agent 작업 수(기본값 3) 값이 클수록 동시 프로브 수가 많아지고 소비량이 많아집니다. 수정 후
              <b>나중에 시작된 작업에 효과적입니다.</b>, 실행 중인 작업은 영향을 받지 않습니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                className="w-32 font-mono text-sm"
                placeholder="3"
                value={workers}
                disabled={!loaded || savingWorkers}
                onChange={(e) => setWorkers(e.target.value)}
              />
              <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
                저장
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyboardIcon className="size-4" />
              세션 입력 상자가 키 입력을 보냅니다.
            </CardTitle>
            <CardDescription>
              대화 상자 페이지와 작업 세부 정보의 기본 Agent 세션 입력 상자는 이 설정을 공유합니다. 선택 후 즉시 적용되며 저장할 필요가 없습니다.
              <br />
              선호도<b>이 브라우저에만 존재합니다.</b>, 계정과 동기화되지 않으며 브라우저를 변경하거나 사이트 데이터를 삭제한 후 재설정해야 합니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
              전송 방법
            </Label>
            <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
              <SelectTrigger id="chat-send-mode" className="w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CHAT_SEND_MODE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
