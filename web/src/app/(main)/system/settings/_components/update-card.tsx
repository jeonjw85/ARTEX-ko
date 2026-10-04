"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { api, sseUrl } from "@/lib/api";
import type { UpdateCheck, UpdateProgress } from "@/lib/types";

/** 새 버전이 출시될 때까지 기다리는 최대 시간입니다. 업그레이드를 시작하려면 세 가지 프로세스(임시 저장소 → 교체 → 새 버전)가 필요합니다.
 *  매번 두 번째 수준에 해당하며 느린 디스크를 처리하고 Docker 컨테이너를 다시 빌드하는 데 3분이면 충분합니다. */
const RESTART_TIMEOUT_MS = 180_000;

function humanSize(n?: number): string {
  if (!n || n <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function UpdateCard() {
  const [info, setInfo] = React.useState<UpdateCheck | null>(null);
  const [checking, setChecking] = React.useState(true);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  // 그리고 progress 분리: 임시저장이 완료된 후 해당 프로세스는 사라집니다.，SSE 중단됩니다. 이때 폴링으로 전환해야 합니다. /api/health。
  const [restarting, setRestarting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  // quiet 동시에 백엔드 캐시 우회 여부 결정: 페이지 진입 시 자동으로 캐시 확인(상단바만 확인함)），
  // 사용자가 수동으로 클릭「업데이트 확인」그런 다음 강제로 소스로 돌아가십시오. 그렇지 않으면 캐시가 만료될 때까지 새로 출시된 버전이 표시되지 않습니다.。
  const check = React.useCallback((quiet = false) => {
    setChecking(true);
    api
      .checkUpdate(!quiet)
      .then((r) => {
        setInfo(r);
        if (!quiet) {
          if (r.error) toast.error("업데이트 확인 실패:" + r.error);
          else if (r.has_update) toast.success(`새 버전 ${r.latest} 발견`);
          else if (r.comparable) toast.success("현재 최신 버전");
        }
      })
      .catch((e) => {
        if (!quiet) toast.error("업데이트 확인 실패:" + (e as Error).message);
      })
      .finally(() => setChecking(false));
  }, []);

  React.useEffect(() => {
    check(true);
  }, [check]);

  // 투표 /api/health 버전 번호가 변경될 때까지。
  //
  // 기준은 다음과 같아야합니다."버전이 변경되었습니다."대신에"연결 가능"：업그레이드가 진행되는 동안 이전 버전이 잠시 다시 시작됩니다.
  // （그땐 내가 책임져야만 했어 artex.new 교체한 후 즉시 종료하세요. 연결 상태만 보면 성공이라고 잘못 판단하게 됩니다.。
  const waitForNewVersion = React.useCallback(async (fromVersion: string) => {
    setRestarting(true);
    const deadline = Date.now() + RESTART_TIMEOUT_MS;
    while (Date.now() < deadline) {
      await sleep(2000);
      try {
        const r = await fetch("/api/health", { cache: "no-store" });
        if (r.ok) {
          const j = (await r.json()) as { version?: string };
          if (j.version && j.version !== fromVersion) {
            toast.success(`${j.version}로 업데이트되었으며 페이지를 다시 로드하는 중입니다.`);
            await sleep(800);
            window.location.reload();
            return;
          }
        }
      } catch {
        // 다시 시작 창 내에 연결할 수 없습니다. 폴링을 계속하세요.。
      }
    }
    setRestarting(false);
    toast.error("서비스 다시 시작을 기다리는 동안 시간이 초과되었습니다. 백엔드 로그를 확인하거나 artex가 start.sh / start.bat를 통해 실행되었는지 확인하세요.");
  }, []);

  // 업데이트 진행 상황을 구독하세요。SSE 떠나지 않음 Next ~의 /api 재작성(해당 레이어는 버퍼링되며 이벤트는 푸시될 수 없음)）。
  const openStream = React.useCallback(
    (fromVersion: string) => {
      const es = new EventSource(sseUrl("/api/update/stream"));
      es.onmessage = (ev) => {
        let p: UpdateProgress;
        try {
          p = JSON.parse(ev.data) as UpdateProgress;
        } catch {
          return;
        }
        setProgress(p);
        if (p.phase === "failed") {
          es.close();
          setBusy(false);
          toast.error("업데이트 실패:" + (p.error || p.message));
          return;
        }
        if (p.phase === "staged") {
          es.close();
          void waitForNewVersion(fromVersion);
        }
      };
      es.onerror = () => {
        // 프로세스가 종료될 때 SSE 연결을 끊어야 합니다. 이미 재부팅을 기다리고 있다면 이는 정상적인 현상입니다.，
        // 넘겨주다 /api/health 계속해서 여론 조사를 하여 결정하세요.。
        es.close();
      };
      return es;
    },
    [waitForNewVersion],
  );

  const doUpdate = () => {
    if (!info) return;
    const from = info.current;
    const ok = window.confirm(
      `${info.latest}로 업데이트하시겠습니까? \n\n` +
        "업데이트하면 프로그램이 다시 시작되고 실행 중인 작업이 중단됩니다. \n" +
        (info.mode === "docker"
          ? "\n 참고: 컨테이너의 업데이트는 프로그램 자체만 대체하며 이미지의 playwright / nmap 및 기타 도구 체인은 업데이트하지 않습니다." +
            "새 버전이 새 도구에 의존하는 경우 대신 docker compose pull를 사용하십시오."
          : ""),
    );
    if (!ok) return;

    setBusy(true);
    setProgress({ phase: "downloading", percent: 0, message: "준비중…" });
    const es = openStream(from);
    api.applyUpdate().catch((e) => {
      es.close();
      setBusy(false);
      setProgress(null);
      toast.error("업데이트를 시작하지 못했습니다:" + (e as Error).message);
    });
  };

  const doRollback = () => {
    if (!info) return;
    if (
      !window.confirm(
        "이전 버전으로 롤백하시겠습니까? \n\n 프로그램이 다시 시작되고 실행 중인 작업이 중단됩니다. \n 참고: 데이터베이스 구조는 롤백되지 않으며 이전 버전은 새 버전에서 작성된 데이터를 인식하지 못할 수 있습니다.",
      )
    )
      return;
    const from = info.current;
    setBusy(true);
    api
      .rollbackUpdate()
      .then(() => {
        toast.success("이전 버전으로 전환하여 다시 시작합니다...");
        void waitForNewVersion(from);
      })
      .catch((e) => {
        setBusy(false);
        toast.error("롤백 실패:" + (e as Error).message);
      });
  };

  const phase = progress?.phase;
  const showProgress = busy || restarting;
  // 다운로드 단계에서만 실제 백분율을 얻을 수 있습니다(누르기 Content-Length 믿다). 확인하다/압축을 푼다/재시작을 기다리는 중
  // 기간을 알 수 없는 단계에서는 진행률 표시줄이 채워지고 이를 표시하기 위해 펄스 애니메이션이 추가됩니다."바쁘지만 얼마나 걸릴지 모르겠어요"。
  const downloading = !restarting && phase === "downloading";
  const pct = downloading ? Math.max(progress?.percent ?? 0, 0) : 100;

  return (
    // 설정 페이지는 다중 열 폭포 흐름 레이아웃입니다. 카드 자체는 줄 간격을 담당하고 열 간 나누기를 금지합니다(참조: page.tsx 댓글）。
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DownloadIcon className="size-4" />
          버전 및 업데이트
        </CardTitle>
        <CardDescription>GitHub에서 새 버전을 확인하고 설치하세요. 업데이트하면 프로그램이 다시 시작되고 실행 중인 작업이 중단됩니다.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">현재 버전</span>
          <Badge variant="secondary" className="font-mono">
            {info?.current ?? "…"}
          </Badge>
          {info && (
            <>
              <Badge variant="outline" className="font-mono">
                {info.os}/{info.arch}
              </Badge>
              <Badge variant="outline">{info.mode === "docker" ? "Docker" : "독립 프로그램"}</Badge>
            </>
          )}
          {info?.latest && (
            <>
              <span className="text-muted-foreground">최신 버전</span>
              <Badge variant={info.has_update ? "default" : "secondary"} className="font-mono">
                {info.latest}
              </Badge>
            </>
          )}
          {info?.html_url && (
            <a
              href={info.html_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline"
            >
              변경 로그 <ExternalLinkIcon className="size-3" />
            </a>
          )}
        </div>

        {info?.boot_notice && (
          <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.boot_notice}
          </p>
        )}

        {info?.error && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            GitHub에 연결할 수 없습니다:{info.error}
            {"　"}위에서 전역 프록시를 구성하고 다시 시도할 수 있습니다.
          </p>
        )}

        {info && !info.comparable && info.reason && <p className="text-xs text-muted-foreground">{info.reason}</p>}

        {info?.has_update && info.asset_available === false && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.latest} 제공되지 않음 {info.os}/{info.arch} 릴리스 패키지(누락 {info.asset})은 자동으로 업데이트될 수 없습니다.
          </p>
        )}

        {info?.has_update && info.asset_available !== false && (
          <p className="text-xs text-muted-foreground">
            다운로드할 것이다 <span className="font-mono">{info.asset}</span>
            {info.size ? `（${humanSize(info.size)}）` : ""}, SHA256를 확인하고 교체하기 전에 연기 테스트를 수행하십시오. 실패하면 현재 버전이 자동으로 유지됩니다.
          </p>
        )}

        {info && !info.has_update && info.comparable && !info.error && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <CheckCircle2Icon className="size-3.5 text-emerald-600" />
            현재 최신 버전입니다.
          </p>
        )}

        {info?.mode === "docker" && info.has_update && (
          <p className="text-xs text-muted-foreground">
            Docker 아래의 업데이트는 프로그램 자체만 교체하며, 이미지의 playwright / nmap와 같은 툴체인은 업데이트하지 않습니다.
            <span className="font-mono"> docker compose up -d </span>
            컨테이너를 다시 빌드한 후에는 이미지와 함께 제공되는 버전이 반환됩니다. 이미지를 함께 업그레이드해야 한다면 그렇게 하세요.
            <span className="font-mono"> docker compose pull artex &amp;&amp; docker compose up -d artex</span>。
          </p>
        )}

        {showProgress && (
          <div className="space-y-1.5">
            <Progress value={pct} className={downloading ? undefined : "animate-pulse"} />
            <p className="text-xs text-muted-foreground">
              {restarting ? "새 버전을 다시 시작하고 적용하는 중입니다. 잠시 기다려 주세요(페이지가 자동으로 새로 고쳐집니다)..." : progress?.message}
            </p>
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => check(false)} disabled={checking || busy || restarting}>
            <RefreshCwIcon className={checking ? "size-4 animate-spin" : "size-4"} />
            업데이트 확인
          </Button>
          <Button
            size="sm"
            onClick={doUpdate}
            disabled={busy || restarting || !info?.has_update || info?.asset_available === false}
          >
            <DownloadIcon className="size-4" />
            {info?.has_update ? `${info.latest}로 업데이트` : "지금 업데이트"}
          </Button>
          {info?.has_backup && (
            <Button variant="ghost" size="sm" onClick={doRollback} disabled={busy || restarting}>
              <RotateCcwIcon className="size-4" />
              이전 버전으로 롤백
            </Button>
          )}
        </div>

        <p className="text-xs text-muted-foreground">
          원클릭 업데이트는 데몬 스크립트에 따라 프로그램을 다시 시작합니다. 통과해주세요 <span className="font-mono">start.sh</span>(Windows는
          <span className="font-mono"> start.bat</span>) ARTEX를 시작합니다. artex 본체를 직접 실행하면 프로그램이 종료된 후 자동으로 실행되지 않습니다.
        </p>
      </CardContent>
    </Card>
  );
}
