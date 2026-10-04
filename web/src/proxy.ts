import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

const AUTH_PAGES = ["/login", "/setup"];

export function proxy(request: NextRequest) {
  // Mock demo: 실제 로그인이 없으며 모든 페이지가 허용됩니다(클라이언트 auth 가드도 허용됩니다).
  if (process.env.NEXT_PUBLIC_MOCK === "1") return NextResponse.next();

  const { pathname } = request.nextUrl;
  const token = request.cookies.get("artex_token")?.value;
  const isAuthPage = AUTH_PAGES.some((p) => pathname === p || pathname.startsWith(`${p}/`));

  // 로그인되지 않음 → 로그인 페이지로 이동
  if (!token && !isAuthPage) {
    return NextResponse.redirect(new URL("/login", request.url));
  }

  // 로그인 후 로그인 방문/초기화 페이지 → 메인 인터페이스로 이동
  if (token && isAuthPage) {
    return NextResponse.redirect(new URL("/function/tasks", request.url));
  }

  return NextResponse.next();
}

export const config = {
  // 뛰어넘다 Next.js 내부 라우팅、API 라우팅、favicon 그리고 public/ 정적 파일(그림, 글꼴 등 포함)）
  matcher: ["/((?!_next/static|_next/image|favicon\\.ico|api/|.*\\.(?:png|jpg|jpeg|gif|webp|svg|ico|woff2?|ttf|otf)$).*)"],
};
