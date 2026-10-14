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
  // 실험적 기능: noa 컨텍스트 압축(기본값은 꺼짐)
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // 순수 프런트 엔드 환경 설정: /api/settings로 이동하지 말고 localStorage를 직접 읽고 쓰세요.
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
      toast.error("동시 횟수는 0보다 큰 정수여야 합니다");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("저장된 동시 작업 에이전트 수(나중에 시작된 작업에 적용)");
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
        toast.success("Python 인터프리터 구성이 저장되었습니다");
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
        toast.success(v ? "에이전트 자동 바인딩 트래픽이 켜져 있습니다" : "에이전트 자동 바인딩 트래픽이 꺼졌습니다");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`저장 실패:${(e as Error).message}`);
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
        toast.success(v ? "noa 컨텍스트 압축이 활성화되었습니다(이후 실행에 적용됨)" : "noa 컨텍스트 압축이 꺼졌습니다(내장 압축 복원)");
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(`저장 실패:${(e as Error).message}`);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("웹 검색 구성이 저장되었습니다");
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
        toast.success("Brave API 키가 저장되었습니다");
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
        toast.success("Tavily API 키가 저장되었습니다");
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
        toast.success(proxyInput.trim() ? "내보내기 프록시가 저장되었습니다" : "내보내기 프록시가 지워졌습니다(직접 연결로 변경됨)");
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
        toast.success(globalProxyInput.trim() ? "글로벌 프록시가 저장되었습니다" : "글로벌 프록시 삭제됨(직접 연결로 변경됨)");
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
        if (r.ok) toast.success(`검색 테스트 성공 · ${r.backend} 반품 ${r.count} 결과`);
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
        <p className="text-muted-foreground text-sm">전체 시스템에 적용되는 설정</p>
      </div>

      {/*그리드 대신 여러 열: 웹 검색 카드는 나머지 카드보다 몇 배 더 크며 높이는 선택한 백엔드에 따라 다릅니다(brave/tavily
          주요 입력은 조건부 렌더링입니다). 그리드는 전체 행을 가장 높은 행으로 채우고 그 옆에 큰 공백을 남깁니다.
          여러 열은 콘텐츠 높이에 따라 균형 있게 자동으로 채워집니다. 카드 간격은 간격 대신 mb에 따라 다름 - 다중 열 레이아웃에서
          column-gap은 열 간격만 관리하며 행 간격은 하위 요소 자체에서 지정해야 합니다. */}
      <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
        <UpdateCard />

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              트래픽 캡처
            </CardTitle>
            <CardDescription>
              켜면 에이전트의 HTTP 트래픽이 기록 프록시를 거쳐 데이터베이스에 저장됩니다. 에이전트는 traffic_search와 traffic_get 도구를 사용할 수 있으며 관련 안내가 프롬프트에 추가됩니다.
              <br />
              끄면 트래픽을 기록하지 않고 프록시 설정, 트래픽 도구, 관련 프롬프트를 에이전트에 제공하지 않습니다. 설정을 바꾸면 에이전트에 즉시 반영됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
              {trafficCapture ? "켜짐 · 트래픽 기록 및 프록시 사용" : "꺼짐 · 트래픽 기록 및 프록시 사용 안 함"}
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
              에이전트의 트래픽 자동 연결
            </CardTitle>
            <CardDescription id="agent-traffic-binding-description">
              켜면 취약점이 등록될 때 보고 에이전트가 기존 HTTP 요청과 응답을 확인하고 관련 트래픽을 연결한 뒤 보고서를 작성합니다.
              <b>트래픽 확인에 추가 도구 호출이 필요하므로 토큰 사용량이 늘어납니다.</b>
              <br />
              TCP 트래픽이거나 캡처한 패킷 또는 일치하는 기록이 없어도 보고서는 작성할 수 있습니다. 트래픽 캡처, 수동 연결, 저장된 증거 조회에는 영향을 주지 않습니다.
              변경 사항은 다음 에이전트 실행부터 적용되며, 끄면 새 자동 연결을 즉시 중단합니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
              {agentTrafficBinding ? "켜짐 · 토큰 사용량 증가" : "꺼짐 · 수동 연결은 계속 가능"}
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
              전체 프록시
            </CardTitle>
            <CardDescription>
              모든 에이전트의 <b>대상 트래픽</b>을 지정한 프록시로 보냅니다. <b>http, https, socks5</b>를 지원하며{" "}
              <code>user:pass</code> 인증 정보를 주소에 넣을 수 있습니다. 비워 두면 직접 연결합니다.
              <br />
              트래픽 캡처가 켜져 있으면 기록 프록시의 업스트림 프록시로 사용합니다. 캡처가 꺼져 있으면 에이전트의 bash와 WebFetch에 프록시 설정을 전달합니다.
              웹 검색과 LLM 연결에는 적용되지 않습니다.
              <br />
              <b>참고:</b> 캡처를 끄고 SOCKS5를 사용하면 명령줄 도구가 <code>ALL_PROXY</code>를 지원해야 합니다.
              일부 도구는 이 설정을 무시할 수 있습니다. SOCKS5를 주로 사용한다면 트래픽 캡처를 켜서 기록 프록시를 거치도록 설정하세요.
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
                placeholder="socks5://user:pass@host:1080 또는 http://host:port(비워두기 = 직접 연결)"
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                저장
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? "설정됨 · 대상 트래픽에 프록시 적용" : "설정되지 않음 · 대상에 직접 연결"}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              작업 제약 조건 전달
            </CardTitle>
            <CardDescription>
              작업 개요에서 설정한 <b>허용 및 금지 조건</b>을 에이전트의 시스템 프롬프트에 전달합니다.
              예를 들어 "현재 포트만 테스트"나 "무차별 대입 금지" 같은 탐색 범위를 지정할 수 있습니다.
              <br />
              <b>플래너</b>와 <b>작업자</b>에 각각 전달할지 선택할 수 있습니다. 둘 다 기본적으로 켜져 있습니다.
              변경 사항은 에이전트를 다시 만들 필요 없이 다음 실행부터 적용됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                플래너에 전달{injectPlanner ? " · 켜짐" : " · 꺼짐"}
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
                작업자에 전달{injectWorker ? " · 켜짐" : " · 꺼짐"}
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
              실험 중인 기능으로 기본값은 꺼짐입니다. 에이전트 동작과 안정성에 영향을 줄 수 있으므로 기능을 이해한 뒤 사용하세요.
              <br />
              <b>noa 컨텍스트 압축</b>은 긴 대화 기록을 모델로 요약하는 기능입니다(norma v0.4.0). 플래너, 작업자, 주 에이전트, 대화 에이전트가
              기본 압축 대신 noa를 사용합니다. 원문은 작업 디렉터리에 보관되어 나중에 확인할 수 있습니다.
              설정 변경은 이후 실행부터 적용되며 에이전트를 다시 만들 필요가 없습니다. 끄면 기본 압축 방식으로 돌아갑니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
              noa 컨텍스트 압축{noaCompaction ? " · 켜짐" : " · 꺼짐"}
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
              웹 검색의 사용 여부와 검색 제공자를 설정합니다. 기능을 켠 뒤 각 에이전트 설정에서 <b>web_search</b> 도구를 개별적으로 활성화할 수 있습니다.
              검색 결과에는 제목, 링크, 요약만 포함되며 본문은 WebFetch로 가져옵니다. 웹 검색은 트래픽 캡처 프록시를 거치지 않습니다.
              <br />
              검색 제공자로 <b>DuckDuckGo(ddgs)</b>(키 불필요), <b>Brave</b>(API 키 필요),{" "}
              <b>Tavily</b>(API 키 필요), <b>DeepSeek</b>(현재 LLM 설정 사용)를 선택할 수 있습니다. 여기서 기능을 끄면
              에이전트별 웹 검색도 사용할 수 없습니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                {webSearch ? "켜짐 · 각 에이전트 설정에서 웹 검색을 활성화할 수 있습니다." : "꺼짐 · 에이전트가 웹 검색을 사용할 수 없습니다."}
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
                <Label className="text-sm font-normal text-muted-foreground">검색 제공자</Label>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="w-48 shrink-0">
                    <SelectValue placeholder="검색 제공자 선택" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">DuckDuckGo(ddgs · 키 없이 무료)</SelectItem>
                    <SelectItem value="brave-free">Brave(무료 버전 · 키 필요)</SelectItem>
                    <SelectItem value="tavily">Tavily(API 키 필요)</SelectItem>
                    <SelectItem value="deepseek">DeepSeek(공식)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {webSearch && backend === "deepseek" && (
              <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                <p className="text-sm font-medium">DeepSeek 공식 온라인 검색</p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  현재 활성화된 LLM 설정을 사용합니다. <b>DeepSeek 공식 모델</b>과 <b>Anthropic 프로토콜</b>이 필요합니다.
                  DeepSeek의 OpenAI 호환 엔드포인트는 서버 측 검색을 지원하지 않습니다. LLM 설정을 바꾸면 이 검색 기능이 작동하지 않을 수 있습니다.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  검색은 <b>DeepSeek 서버에서 실행</b>됩니다. 검색할 때마다 모델 호출에 따른 토큰 요금이 발생합니다.
                  요청은 위의 프록시 설정을 거치지 않으며 트래픽 기록에도 남지 않습니다. 결과에는 <b>제목과 링크만</b> 포함됩니다.
                  본문이 필요하면 WebFetch를 사용하세요.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  설정이 맞는지 아래의 "검색 테스트" 버튼으로 확인하세요. 시스템은 설정 오류를 사전에 차단하지 않습니다.
                </p>
              </div>
            )}

            {webSearch && backend === "brave-free" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                  Brave Search API Key
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">구성됨</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="brave-key"
                    type="password"
                    autoComplete="off"
                    placeholder={braveKeySet ? "구성됨(변경되지 않은 상태로 유지하려면 비워두세요)" : "Brave API 키 입력"}
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
                    Brave가 선택되었지만 키가 구성되지 않았습니다. 키가 저장될 때까지 검색 도구가 활성화되지 않습니다.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">
                  무료 버전은 월 2,000회 정도로 제한되어 있습니다. 키를 받으려면 https://brave.com/search/api/로 이동하세요.
                </p>
              </div>
            )}

            {webSearch && backend === "tavily" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                  Tavily Search API Key
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">구성됨</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="tavily-key"
                    type="password"
                    autoComplete="off"
                    placeholder={tavilyKeySet ? "구성됨(변경되지 않은 상태로 유지하려면 비워두세요)" : "Tavily API 키 입력(tvly-…)"}
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
                    Tavily가 선택되었지만 키가 구성되지 않았습니다. 키가 저장될 때까지 검색 도구가 활성화되지 않습니다.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">https://tavily.com으로 이동하여 등록하고 API 키를 받으세요.</p>
              </div>
            )}

            {webSearch && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                  프록시 내보내기(선택사항)
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder="http://host:port 또는 Socks5://host:port(비워두기 = 직접 연결)"
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    저장
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">
                  검색 엔드포인트(VPN/SOCKS 등)에 액세스하는 데만 사용되는 별도의 송신 프록시입니다. 트래픽을 기록하는 MITM 프록시와는 아무런 관련이 없습니다. 네트워크를 사용할 수 없을 때 이 프록시를 통해 액세스됩니다.
                </p>
              </div>
            )}

            {webSearch && (
              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <p className="text-muted-foreground text-xs">
                  현재 구성(소스 + 에이전트 + 키)을 사용하여 실제로 "테스트"를 검색하여 사용 가능한지 확인합니다.
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={runTest}
                  disabled={!loaded || testing}
                  className="shrink-0"
                >
                  {testing ? "테스트 중..." : "검색 테스트"}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              맞춤 스크립트 · Python 인터프리터
            </CardTitle>
            <CardDescription>
              맞춤 <b>script</b> 도구가 사용할 Python 실행 파일을 지정합니다. 비워 두면 실행 시 자동으로 찾으며 python3을 우선 사용합니다.
              가상 환경이나 특정 버전을 사용하려면 실행 파일의 절대 경로를 입력하세요.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                className="font-mono text-sm"
                placeholder="/usr/bin/python3 (비워두기 = 자동 감지)"
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                다시 확인
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
              작업 동시성 · 작업 에이전트 수
            </CardTitle>
            <CardDescription>
              작업마다 동시에 실행할 작업자 에이전트의 수입니다(기본값 3). 값을 높이면 동시 요청과 리소스 사용량이 늘어납니다.
              변경 사항은 <b>이후에 시작하는 작업</b>부터 적용됩니다.
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
              세션 입력 상자에서 키 입력 보내기
            </CardTitle>
            <CardDescription>
              대화 화면과 작업 상세 화면의 에이전트 세션 입력창에 함께 적용됩니다. 선택하면 즉시 적용되며 별도로 저장할 필요가 없습니다.
              <br />
              이 설정은 <b>현재 브라우저에만 저장</b>됩니다. 다른 브라우저에서 접속하거나 사이트 데이터를 지우면 다시 설정해야 합니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
              보내는 방법
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
