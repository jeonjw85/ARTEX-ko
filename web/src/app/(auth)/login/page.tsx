"use client";

import { useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { AlertTriangle, ShieldCheck } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function LoginPage() {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [agreed, setAgreed] = useState(false);
  const [termsOpen, setTermsOpen] = useState(false);
  const [readToEnd, setReadToEnd] = useState(false);
  const termsBodyRef = useRef<HTMLDivElement>(null);

  // "동의"를 클릭하기 전에 약관 하단(스크롤 없이 전체 표시 포함)으로 스크롤하세요.
  function handleTermsScroll() {
    const el = termsBodyRef.current;
    if (!el) return;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8) setReadToEnd(true);
  }

  useEffect(() => {
    if (!termsOpen) return;
    // 열 때 재설정하고 콘텐츠가 한 화면 미만이고 스크롤을 실행할 수 없는 시나리오를 처리합니다.
    setReadToEnd(false);
    const el = termsBodyRef.current;
    if (el && el.scrollHeight <= el.clientHeight + 8) setReadToEnd(true);
  }, [termsOpen]);

  useEffect(() => {
    // 로그인하고 기본 인터페이스로 직접 들어갑니다(정적 내보내기에서 이 점프를 수행할 middleware가 없습니다).
    const token = auth.getToken();
    if (token) {
      // localStorage에는 아직 자격 증명이 있을 수 있지만 cookie는 자격 증명을 잃어버렸습니다. 먼저 동기화한 다음 새 요청을 시작하세요.
      // 서버 측 가드 또는 경로 캐시가 여전히 checking 상태에 있는 로그인 페이지로 다시 리디렉션되지 않도록 방지합니다.
      auth.setToken(token);
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError("백엔드 서비스에 연결할 수 없습니다."))
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!agreed) {
      setError("먼저 ＂사용 지침＂을 읽고 동의하십시오.");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.login("ARTEX", password);
      auth.setToken(token);
      window.location.replace("/function/tasks");
    } catch {
      setError("잘못된 사용자 이름 또는 비밀번호");
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div role="status" className="flex min-h-dvh items-center justify-center text-muted-foreground">
        로그인 상태를 확인하는 중...
      </div>
    );
  }

  return (
    <div className="flex h-dvh">
      {/* Left panel */}
      <div className="hidden flex-col items-center justify-center bg-primary p-12 text-center lg:flex lg:w-1/3">
        <div className="relative flex items-center justify-center">
          <div className="absolute size-80 rounded-full border border-primary-foreground/10" />
          <div className="absolute size-60 rounded-full border border-primary-foreground/15" />
          <div className="absolute size-40 rounded-full border border-primary-foreground/20" />
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src="/logo.png" alt="ARTEX" width={160} height={160} className="relative brightness-0 invert" />
        </div>
      </div>

      {/* Right panel */}
      <div className="flex w-full items-center justify-center bg-background p-8 lg:w-2/3">
        <div className="w-full max-w-md space-y-10 py-24 lg:py-32">
          <div className="space-y-4 text-center">
            <h2 className="text-2xl font-medium tracking-tight">로그인</h2>
            <p className="mx-auto max-w-xl text-muted-foreground">다시 오신 것을 환영합니다. ARTEX를 계속 사용하려면 비밀번호를 입력하세요.</p>
          </div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">사용자 이름</Label>
              <Input id="username" value="ARTEX" readOnly className="bg-muted text-muted-foreground" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">비밀번호</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="비밀번호를 입력해주세요"
                autoFocus
                autoComplete="current-password"
              />
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="agree-terms"
                checked={agreed}
                onCheckedChange={(v) => setAgreed(v === true)}
                className="mt-0.5"
              />
              <Label htmlFor="agree-terms" className="text-sm font-normal leading-relaxed text-muted-foreground">
                읽었으며 이에 동의합니다.
                <button
                  type="button"
                  onClick={() => setTermsOpen(true)}
                  className="mx-0.5 font-medium text-primary underline-offset-4 hover:underline"
                >
                  "사용 지침"
                </button>
              </Label>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={loading || !password || !agreed}>
              {loading ? "로그인..." : "로그인"}
            </Button>
          </form>
        </div>
      </div>

      <Dialog open={termsOpen} onOpenChange={setTermsOpen}>
        <DialogContent className="gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="flex-row items-center gap-3 border-b px-6 py-4">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <ShieldCheck className="size-5" />
            </div>
            <div className="space-y-0.5">
              <DialogTitle className="text-base">ARTEX 사용 지침 및 면책 조항</DialogTitle>
              <p className="text-xs text-muted-foreground">
                버전 v1.0 · 시행일 2026-09-18 · 로그인하기 전에 다음 약관을 모두 읽어보시기 바랍니다.
              </p>
            </div>
          </DialogHeader>

          <div
            ref={termsBodyRef}
            onScroll={handleTermsScroll}
            className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
          >
            <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
              본 "사용 지침 및 면책 조항"(이하 "본 성명")은 귀하와 ARTEX 사이에 있습니다.
              이 소프트웨어 사용에 관한 프로젝트 작성자와 기여자 간의 계약입니다. 사용하기 전에 각 조항의 내용, 특히 굵은 글씨나 색상 블록으로 표시된 면책 조항, 책임 제한 및 금지 조항을 주의 깊게 읽고 완전히 이해하십시오.
              <span className="font-medium text-foreground">
                {" "}
                어떤 방식으로든 이 소프트웨어를 다운로드, 설치, 액세스 또는 사용하면 귀하는 이 모든 내용을 읽고 이해했으며 이에 동의한 것으로 간주됩니다.
              </span>
            </p>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  1
                </span>
                제 1조 · 정의 및 오픈 소스 라이선스
              </h4>
              <p className="pl-7">
                이 소프트웨어(ARTEX)는 GNU Affero General Public License를 기반으로 한 소프트웨어입니다.
                v3.0(AGPL-3.0)에서 출시한 오픈 소스 프로그램입니다. 귀하는 본 계약에 따라 본 소프트웨어를 자유롭게 사용, 복사, 수정 및 배포할 수 있습니다. 그러나 모든 파생 저작물(네트워크를 통해 제3자에게 제공되는 온라인 서비스 포함)도
                AGPL-3.0 프로토콜은 오픈 소스이며 해당 전체 소스 코드가 사용자에게 공개됩니다. AGPL-3.0의 전체 조건은 함께 제공되는 LICENSE 문서의 적용을 받습니다.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  2
                </span>
                제2조 · 승인된 사용 범위
              </h4>
              <p className="pl-7">
                이 소프트웨어는 귀하가 구축한 로컬 격리 환경에서의 개인 연구, 코드 연구, 보안 기술 원칙 토론 및 기술 검증용으로만 사용됩니다. 학습, 학술 연구, 코드 검토 등 공격적이지 않고 파괴적이지 않은 목적에 적합합니다. 귀하는 이 섹션에서 명시적으로 허용된 것 이외의 다른 목적으로 소프트웨어를 사용할 수 없습니다.
              </p>
            </section>

            <section className="space-y-2">
              <h4 className="flex items-center gap-2 font-medium text-destructive">
                <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
                  3
                </span>
                <AlertTriangle className="size-4" />
                제3조 · 금지된 행위
              </h4>
              <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
                <li>
                  다른 사람이나 제3자가 소유한 웹사이트, 온라인 서비스 또는 네트워크 시스템(귀하의 승인 또는 소유 여부에 관계없이)에 대한 스캐닝, 감지, 악용 또는 공격을 시작하는 것은 엄격히 금지됩니다.
                </li>
                <li>실제 침투 테스트, 공격 및 방어 대결, 레드 앤 블루 연습 또는 생산 환경에 이 소프트웨어를 사용하는 것은 엄격히 금지됩니다.</li>
                <li>불법 침입, 데이터 도난, 강탈, 서비스 거부(DoS/DDoS) 또는 파괴적이거나 범죄적인 활동에 이 소프트웨어를 사용하는 것은 엄격히 금지되어 있습니다.</li>
                <li>소프트웨어 및 그 출력물에 포함된 저작권, 라이센스 또는 보안 공지 정보를 제거, 변조 또는 우회하는 것은 엄격히 금지됩니다.</li>
                <li>해당 국가 또는 지역의 법률, 규정 및 규제 요구 사항을 위반하는 행위에 가담하는 것은 엄격히 금지됩니다.</li>
              </ul>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  4
                </span>
                제4조 · 지적재산권
              </h4>
              <p className="pl-7">
                이 소프트웨어의 저작권 및 관련 지적 재산권은 프로젝트 작성자 및 기여자에게 속하며 AGPL-3.0에 따라 관리됩니다.
                해당 권리는 계약 범위 내에서 귀하에게 부여됩니다. 본 정책은 본 계약에서 명시적으로 부여된 권리를 제외하고 명시적이든 묵시적이든 귀하에게 다른 권리를 부여하지 않습니다.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  5
                </span>
                제5조 · 데이터 및 개인정보 보호
              </h4>
              <p className="pl-7">
                이 소프트웨어는 자체 배포 가능한 오픈 소스 프로그램입니다. 작성자는 중앙 집중식 서비스를 운영하지 않으며 귀하의 사용 데이터를 수집하거나 업로드하지 않습니다. 사용 중에 생성, 처리 또는 접촉된 모든 데이터의 적법성과 보안에 대한 책임은 전적으로 귀하에게 있습니다. 부적절한 데이터 처리로 인해 발생하는 모든 결과에 대한 책임은 전적으로 귀하에게 있습니다.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  6
                </span>
                제6조 · 규정 준수 및 법적 책임
              </h4>
              <p className="pl-7">
                귀하는 네트워크 보안, 데이터 보안, 개인 정보 보호, 컴퓨터 범죄 등과 관련하여 귀하가 위치한 국가 또는 지역의 모든 법률 및 규정을 준수할 책임이 있습니다. (중국 본토의 경우 여기에는 사이버 보안법, 데이터 보안법, 개인 정보 보호법 및 관련 사법 해석이 포함되지만 이에 국한되지 않습니다.)
                <span className="font-medium text-foreground">
                  {" "}
                  귀하가 위의 법률 및 규정이나 이 성명의 조항을 위반함으로써 발생하는 모든 법적 책임과 결과는 전적으로 귀하가 부담해야 하며 이 소프트웨어의 작성자 및 기여자와는 아무런 관련이 없습니다.
                </span>
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  7
                </span>
                제7조 · 책임의 부인 및 제한
              </h4>
              <p className="pl-7">
                이 소프트웨어는 "현재 상태(AS IS)"와 "기존(AS)"을 기반으로 합니다.
                AVAILABLE)" 상태는 상품성, 특정 목적에의 적합성, 정확성 및 비침해에 대한 보증을 포함하되 이에 국한되지 않는 어떠한 명시적 또는 묵시적 보증 없이 제공됩니다. 해당 법률이 허용하는 최대 한도 내에서 이 소프트웨어의 작성자 및 기여자는 이 소프트웨어의 사용 또는 사용 불가능으로 인해 발생하는 직간접적, 우발적, 특별 또는 결과적 손실에 대해 책임을 지지 않습니다(사용이 부적절한지 여부에 관계없이). 데이터 손실, 시스템 손상, 업무 중단, 이익 손실 또는 법적 분쟁에 국한되지 않습니다.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  8
                </span>
                제8조 · 약관의 변경 및 최종 해석
              </h4>
              <p className="pl-7">
                저자는 법률, 규정 또는 프로젝트 개발 요구에 따라 수시로 이 선언문을 업데이트할 권리가 있습니다. 업데이트된 버전은 프로젝트와 함께 출시되며 출시일부터 유효합니다. 이 소프트웨어를 계속 사용하면 개정된 약관에 동의한 것으로 간주됩니다. 법이 허용하는 한도 내에서 이 설명의 최종 해석 권한은 프로젝트 작성자에게 있습니다. 본 정책의 일부 조항이 유효하지 않은 것으로 판명되더라도 나머지 조항의 유효성은 영향을 받지 않습니다.
              </p>
            </section>
          </div>

          <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-xs text-muted-foreground">
              {readToEnd ? "모든 약관을 확인하셨습니다." : "확인하기 전에 약관을 아래로 스크롤하세요."}
            </p>
            <DialogClose asChild>
              <Button
                type="button"
                disabled={!readToEnd}
                onClick={() => {
                  setAgreed(true);
                  setError("");
                }}
              >
                나는 모든 약관을 읽었으며 이에 동의합니다.
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
