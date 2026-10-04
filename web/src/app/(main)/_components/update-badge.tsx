"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";

/**
 * 상단 표시줄의 "새 버전 사용 가능" 프롬프트: 전체 페이지가 로드될 때 한 번 확인하세요. 업데이트가 있으면 버전 번호 옆에 강조 표시됩니다.
 * 시스템 구성 페이지로 직접 이동하려면 "버전 및 업데이트" 카드를 클릭하세요.
 *
 * 백엔드는 GitHub의 쿼리 결과를 30분 동안 캐싱하므로 여기에 마운트할 때마다 확인하는 것이 안전하다.
 * ——인증되지 않은 GitHub API에는 60회/시간/IP만 있습니다. 캐시 레이어가 없으면 탭을 몇 개 더 엽니다.
 * 할당량은 다 소진될 것이고, 나중에 꼭 업데이트하고 싶은데 그럴 수 없습니다.
 *
 * 쿼리가 실패하면 조용해집니다. 상단 표시줄은 오류를 보고하는 곳이 아닙니다. 사용자는 설정 페이지로 이동하여 "업데이트 확인"을 클릭하여 이유를 확인할 수 있습니다.
 */
export function UpdateBadge() {
  const [latest, setLatest] = React.useState("");

  React.useEffect(() => {
    let alive = true;
    api
      .checkUpdate()
      .then((r) => {
        // has_update에는 이미 "비교 가능한 버전 번호" 판단이 포함되어 있으며 개발 빌드 중에는 이 프롬프트가 표시되지 않습니다.
        if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
      })
      .catch(() => {
        // 무음: 네트워크 없음 / GitHub 전류가 제한되어 있어도 상단 표시줄에 오류가 팝업되어서는 안 됩니다.
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      title={`새 버전 ${latest}를 찾았습니다. 업데이트하려면 클릭하세요.`}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* 호흡 포인트: 상단 바에는 많은 요소가 있습니다. 순수한 텍스트는 무시되기 쉽습니다. 다이나믹한 효과로 한눈에 알아볼 수 있습니다.。 */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">새 버전 {latest}</span>
      <span className="sm:hidden">새 버전</span>
    </Link>
  );
}
