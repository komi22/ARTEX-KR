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

  // "동의"를 클릭하기 전에 약관 하단으로 스크롤하세요(스크롤 없는 전체 표시 포함).
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
    // 로그인한 후 기본 인터페이스에 직접 들어갈 수 있습니다(정적 내보내기에서는 미들웨어가 이 점프를 수행하지 않습니다).
    const token = auth.getToken();
    if (token) {
      //localStorage에는 여전히 자격 증명이 있을 수 있지만 쿠키는 손실됩니다. 먼저 동기화한 다음 새 요청을 시작하세요.
      // 서버 측 가드 또는 경로 캐시가 아직 확인 상태에 있는 로그인 페이지로 다시 리디렉션되지 않도록 방지합니다.
      auth.setToken(token);
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError("서버에 연결할 수 없습니다"))
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!agreed) {
      setError("먼저 이용 안내를 읽고 동의해 주세요");
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
        로그인 상태 확인 중...
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
            <p className="mx-auto max-w-xl text-muted-foreground">다시 오신 것을 환영합니다. ARTEX를 계속 사용하려면 비밀번호를 입력하세요</p>
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
                placeholder="비밀번호를 입력하세요"
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
                다음 내용을 읽고 동의합니다
                <button
                  type="button"
                  onClick={() => setTermsOpen(true)}
                  className="mx-0.5 font-medium text-primary underline-offset-4 hover:underline"
                >
                  이용 안내
                </button>
              </Label>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={loading || !password || !agreed}>
              {loading ? "로그인 중..." : "로그인"}
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
              <DialogTitle className="text-base">ARTEX 이용 안내 및 면책 고지</DialogTitle>
              <p className="text-xs text-muted-foreground">
                버전 v1.0 · 시행일 2026-09-18 · 로그인 전에 모든 내용을 읽어 주세요.
              </p>
            </div>
          </DialogHeader>

          <div
            ref={termsBodyRef}
            onScroll={handleTermsScroll}
            className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
          >
            <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
              이 이용 안내 및 면책 고지(이하 “본 고지”)는 ARTEX 사용에 관해 사용자와 프로젝트 작성자 및 기여자 사이에 적용됩니다. 사용하기 전에 각 조항, 특히 강조 표시된 면책, 책임 제한 및 금지 사항을 주의 깊게 읽고 이해해 주세요.
              <span className="font-medium text-foreground">
                {" "}
                이 소프트웨어를 다운로드, 설치, 접속하거나 그 밖의 방식으로 사용하면 본 고지의 모든 내용을 읽고 이해했으며 이에 동의한 것으로 간주됩니다.
              </span>
            </p>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  1
                </span>
                제1조 · 정의와 오픈소스 라이선스
              </h4>
              <p className="pl-7">
                ARTEX는 GNU Affero General Public License v3.0(AGPL-3.0)에 따라 배포되는 오픈소스 프로그램입니다. 이 라이선스에 따라 소프트웨어를 사용, 복사, 수정 및 배포할 수 있습니다. 파생 저작물에는 네트워크를 통해 제3자에게 제공하는 온라인 서비스도 포함되며, AGPL-3.0에 따라 해당 소스 코드 전체를 사용자에게 공개해야 합니다. 라이선스의 정확한 조건은 함께 제공되는 LICENSE 파일을 확인하세요.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  2
                </span>
                제2조 · 허용되는 사용 범위
              </h4>
              <p className="pl-7">
                이 소프트웨어는 개인 학습, 코드 연구, 보안 기술의 원리 탐구, 사용자가 직접 구축한 격리된 로컬 환경에서의 기술 검증에 한해 사용할 수 있습니다. 학술 연구나 코드 검토 등 공격적이거나 파괴적이지 않은 용도를 위한 것입니다. 이 조항에서 명시적으로 허용하지 않은 목적으로는 사용할 수 없습니다.
              </p>
            </section>

            <section className="space-y-2">
              <h4 className="flex items-center gap-2 font-medium text-destructive">
                <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
                  3
                </span>
                <AlertTriangle className="size-4" />
                제3조 · 금지 행위
              </h4>
              <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
                <li>
                  웹사이트, 온라인 서비스 또는 인터넷에 연결된 시스템을 스캔, 탐색, 악용하거나 공격해서는 안 됩니다. 대상의 소유권이나 허가 여부와 관계없이 적용됩니다.
                </li>
                <li>실제 침투 테스트, 공격·방어 훈련, 레드팀·블루팀 훈련 또는 운영 환경에서 사용해서는 안 됩니다.</li>
                <li>불법 침입, 데이터 절취, 금전 갈취, 서비스 거부(DoS/DDoS) 또는 기타 파괴적이거나 범죄적인 행위에 사용해서는 안 됩니다.</li>
                <li>소프트웨어나 그 결과물에 포함된 저작권, 라이선스 또는 보안 고지를 제거, 변경하거나 우회해서는 안 됩니다.</li>
                <li>거주 국가나 지역의 법률, 규정 또는 감독 요건을 위반해서는 안 됩니다.</li>
              </ul>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  4
                </span>
                제4조 · 지식재산권
              </h4>
              <p className="pl-7">
                소프트웨어의 저작권과 관련 지식재산권은 프로젝트 작성자 및 기여자에게 있습니다. 사용자에게는 AGPL-3.0에서 정한 범위의 권리가 부여됩니다. 본 고지는 해당 라이선스에 명시되지 않은 권리를 추가로 부여하지 않습니다.
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
                이 소프트웨어는 직접 배포해 사용할 수 있는 오픈소스 프로그램입니다. 작성자는 중앙 서비스를 운영하지 않으며 사용 데이터를 수집하거나 업로드하지 않습니다. 사용 중 생성하거나 처리하거나 접하는 데이터의 적법성과 보안, 부적절한 데이터 처리로 인한 결과는 사용자가 책임집니다.
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
                사용자는 거주 국가나 지역의 사이버 보안, 데이터 보안, 개인정보 보호 및 컴퓨터 범죄 관련 법령을 준수해야 합니다. 중국 본토에서는 사이버보안법, 데이터보안법, 개인정보보호법 및 관련 사법 해석 등이 여기에 포함됩니다.
                <span className="font-medium text-foreground">
                  {" "}
                  위 법령이나 본 고지를 위반해 발생한 법적 책임과 결과는 사용자 본인이 부담하며, 소프트웨어 작성자 및 기여자는 이에 대한 책임을 지지 않습니다.
                </span>
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  7
                </span>
                제7조 · 면책 및 책임 제한
              </h4>
              <p className="pl-7">
                소프트웨어는 "있는 그대로(AS IS)" 및 "제공 가능한 상태(AS AVAILABLE)"로 제공되며, 상품성, 특정 목적 적합성, 정확성, 비침해성을 포함한 명시적·묵시적 보증이 없습니다. 관련 법률이 허용하는 최대 범위에서 작성자와 기여자는 소프트웨어의 사용 또는 사용 불가로 인한 직접적·간접적·부수적·특별·결과적 손해에 책임을 지지 않습니다. 여기에는 데이터 손실, 시스템 손상, 업무 중단, 이익 손실 및 법적 분쟁이 포함되며, 사용 방법의 적절성과 관계없이 적용됩니다.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  8
                </span>
                제8조 · 고지의 변경과 해석
              </h4>
              <p className="pl-7">
                작성자는 법령이나 프로젝트 개발에 따라 본 고지를 변경할 수 있습니다. 변경 내용은 프로젝트와 함께 공개되며 공개일부터 효력이 발생합니다. 계속 사용하면 변경된 내용에 동의한 것으로 간주됩니다. 법률이 허용하는 범위에서 최종 해석 권한은 프로젝트 작성자에게 있습니다. 일부 조항이 무효로 판단되더라도 나머지 조항의 효력은 유지됩니다.
              </p>
            </section>
          </div>

          <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-xs text-muted-foreground">
              {readToEnd ? "모든 내용을 확인했습니다" : "동의하려면 끝까지 스크롤해 주세요"}
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
                모든 내용을 읽었으며 동의합니다
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
