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
 *  각 시간은 몇 초 정도 소요되며 느린 디스크와 Docker 컨테이너 재구성을 처리하는 데 3분이면 충분합니다. */
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
  // 진행과 분리: 임시 저장이 완료된 후 프로세스가 사라지고 SSE가 중단됩니다. 이때 /api/health 폴링으로 전환해야 합니다.
  const [restarting, setRestarting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  // Quiet은 백엔드 캐시를 우회할지 여부도 결정합니다. 페이지에 들어갈 때 자동으로 캐시를 확인합니다(상단 표시줄에서 확인함).
  // 사용자는 "업데이트 확인"을 수동으로 클릭하여 소스로 강제로 돌아갑니다. 그렇지 않으면 캐시가 만료될 때까지 새로 출시된 버전이 표시되지 않습니다.
  const check = React.useCallback((quiet = false) => {
    setChecking(true);
    api
      .checkUpdate(!quiet)
      .then((r) => {
        setInfo(r);
        if (!quiet) {
          if (r.error) toast.error("업데이트 확인 실패:" + r.error);
          else if (r.has_update) toast.success(`새 버전 발견 ${r.latest}`);
          else if (r.comparable) toast.success("최신 버전입니다");
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

  // 버전 번호가 변경될 때까지 /api/health를 폴링합니다.
  //
  //기준은 "연결 가능"이 아닌 "버전이 변경되었습니다"여야 합니다. 이전 버전은 변경 프로세스 중에 잠시 다시 시작됩니다.
  // (당시에는 artex.new를 교체하고 즉시 종료하는 역할만 담당했습니다.) 연결성만 보면 성공을 잘못 판단하게 됩니다.
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
            toast.success(`업데이트됨 ${j.version}, 페이지 새로고침`);
            await sleep(800);
            window.location.reload();
            return;
          }
        }
      } catch {
        // 다시 시작 창 내에 연결 실패가 예상되며 폴링이 계속됩니다.
      }
    }
    setRestarting(false);
    toast.error("서비스 재시작을 기다리는 동안 시간이 초과되었습니다. 백엔드 로그를 확인하거나 start.sh / start.bat를 통해 artex가 시작되었는지 확인하세요.");
  }, []);

  // 업데이트 진행 상황을 구독하세요. SSE는 Next의 /api 재작성을 사용하지 않습니다(해당 레이어는 버퍼링되며 이벤트는 푸시될 수 없습니다).
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
        // 프로세스가 종료되면 SSE의 연결이 끊어져야 합니다. 이미 재부팅을 기다리고 있다면 이는 정상적인 현상입니다.
        // 폴링하고 결정을 계속하려면 /api/health에 그대로 두세요.
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
      `${info.latest} 버전으로 업데이트할까요?\n\n` +
        "업데이트하면 프로그램이 다시 시작되고 실행 중인 작업이 중단됩니다.\n" +
        (info.mode === "docker"
          ? "\n참고: 컨테이너 안에서 업데이트하면 ARTEX 실행 파일만 교체됩니다. Playwright, nmap 등 이미지에 설치된 도구도 업데이트하려면 docker compose pull을 사용하세요."
          : ""),
    );
    if (!ok) return;

    setBusy(true);
    setProgress({ phase: "downloading", percent: 0, message: "준비 중..." });
    const es = openStream(from);
    api.applyUpdate().catch((e) => {
      es.close();
      setBusy(false);
      setProgress(null);
      toast.error("업데이트 시작 실패:" + (e as Error).message);
    });
  };

  const doRollback = () => {
    if (!info) return;
    if (
      !window.confirm(
        "이전 버전으로 롤백하시겠습니까?\n\n프로그램이 다시 시작되고 실행 중인 작업이 중단됩니다.\n참고: 데이터베이스 구조는 롤백되지 않으며 이전 버전은 새 버전에서 작성된 데이터를 인식하지 못할 수 있습니다.",
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
  // 다운로드 단계에서만 실제 백분율(Content-Length에 따라 계산됨)을 얻을 수 있습니다. 확인/압축해제/재시작 대기 모두
  // 기간을 알 수 없는 단계에서는 진행률 표시줄이 채워지고 "사용 중이지만 시간이 얼마나 걸릴지 확실하지 않음"을 나타내는 펄스 애니메이션이 추가됩니다.
  const downloading = !restarting && phase === "downloading";
  const pct = downloading ? Math.max(progress?.percent ?? 0, 0) : 100;

  return (
    // 설정 페이지는 다중 열 폭포 레이아웃입니다. 카드 자체는 줄 간격을 담당하고 열 간 나누기를 금지합니다(page.tsx의 설명 참조).
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
              <Badge variant="outline">{info.mode === "docker" ? "Docker" : "독립형 프로그램"}</Badge>
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
              업데이트 로그 <ExternalLinkIcon className="size-3" />
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
            {info.latest} 제공되지 않음 {info.os}/{info.arch} 용 릴리스 패키지(누락 {info.asset}), 자동으로 업데이트할 수 없습니다.
          </p>
        )}

        {info?.has_update && info.asset_available !== false && (
          <p className="text-xs text-muted-foreground">
            <span className="font-mono">{info.asset}</span> 파일
            {info.size ? `(${humanSize(info.size)})` : ""}을 내려받아 SHA-256과 실행 상태를 확인한 뒤 교체합니다. 실패하면 현재 버전을 유지합니다.
          </p>
        )}

        {info && !info.has_update && info.comparable && !info.error && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <CheckCircle2Icon className="size-3.5 text-emerald-600" />
            최신 버전입니다.
          </p>
        )}

        {info?.mode === "docker" && info.has_update && (
          <p className="text-xs text-muted-foreground">
            Docker 안에서 업데이트하면 ARTEX 실행 파일만 교체됩니다. Playwright, nmap 등 이미지의 도구는 그대로 유지됩니다.
            <span className="font-mono"> docker compose up -d </span>로 컨테이너를 다시 만들면 이미지에 포함된 버전으로 돌아갈 수 있습니다.
            이미지를 함께 업데이트하려면 <span className="font-mono">docker compose pull artex &amp;&amp; docker compose up -d artex</span>를 실행하세요.
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
            {info?.has_update ? `업데이트 ${info.latest}` : "지금 업데이트"}
          </Button>
          {info?.has_backup && (
            <Button variant="ghost" size="sm" onClick={doRollback} disabled={busy || restarting}>
              <RotateCcwIcon className="size-4" />
              이전 버전으로 롤백
            </Button>
          )}
        </div>

        <p className="text-xs text-muted-foreground">
          화면에서 업데이트하려면 <span className="font-mono">start.sh</span>(Windows는
          <span className="font-mono"> start.bat</span>)로 ARTEX를 실행하세요. 실행 파일을 직접 시작하면 업데이트 후 자동으로 재시작되지 않습니다.
        </p>
      </CardContent>
    </Card>
  );
}
