import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// 시트와 대화 상자의 onInteractOutside가 닫힘 여부를 판단할 때 사용하는 보조 함수입니다.
//
// 배경: 서랍의 Radix 팝업 레이어(Select drop-down, DropdownMenu, Popover 등)가 서랍으로 포탈됩니다.
// 바깥쪽에 나타납니다. 팝업이 열린 상태에서 배경을 누르면 이 포인터 이벤트를 Select와 Sheet의
// DismissableLayer는 동시에 처리됩니다. Select가 먼저 닫히고 개별 이벤트이므로 React는 동기적으로 플러시됩니다.
// Sheet의 프로세서 차례가 되면 팝업 레이어의 데이터 상태는 이미 닫힌 상태로 전환되었습니다. 즉, 팝업 레이어가 "현재" 열려 있는지 여부를 감지합니다.
// 당연히 신뢰할 수 없습니다(실제 측정으로 입증됨).
//
// 올바른 접근 방식: Radix의 포인터 다운 모니터링은 버블링 단계에 있습니다. 먼저 캡처 단계(이전)에 넣습니다.
// "지금 열려 있는 탄력적 레이어가 있습니까?"라는 메시지가 기록되고, onInteractOutside는 이 레코드 값을 읽어 클로저를 해제할지 여부를 결정합니다.
function isRadixOverlayOpenNow(): boolean {
  if (typeof document === "undefined") return false;
  return !!document.querySelector(
    [
      "[data-slot='select-trigger'][data-state='open']",
      "[data-slot='select-content'][data-state='open']",
      "[role='listbox'][data-state='open']",
      "[data-radix-popper-content-wrapper]",
      "[aria-expanded='true'][data-state='open']",
    ].join(","),
  );
}

let overlayOpenAtLastPointerDown = false;
if (typeof document !== "undefined") {
  document.addEventListener(
    "pointerdown",
    () => {
      overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
    },
    true, // 캡처: Radix 버블링 단계에서 포인터다운 프로세서 이전에 기록합니다.
  );
}

// radixOverlayWasOpenAtPointerDown은 "마지막 포인터다운이 발생했을 때 Radix 팝업 레이어가 있었는지 여부"를 반환합니다.
//열기". 서랍/대화 상자는 다음을 기반으로 합니다. 탄성 레이어가 열려 있으면 마스크를 클릭하고 → 탄성 레이어만 닫고 자체는 닫지 않습니다.
export function radixOverlayWasOpenAtPointerDown(): boolean {
  return overlayOpenAtLastPointerDown;
}

// copyText는 클립보드에 텍스트를 쓰고 성공 여부를 반환합니다.
// 배경: navigator.clipboard는 보안 컨텍스트(HTTPS/localhost)에서만 사용할 수 있습니다. IP + HTTP를 통해
// 액세스 시 정의되지 않으며 execCommand("copy")로 다운그레이드됩니다.
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // 다운그레이드 계획 계속하기
    }
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    textarea.style.top = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}

export const getInitials = (str: string): string => {
  if (typeof str !== "string" || !str.trim()) return "?";

  return (
    str
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .map((word) => word[0])
      .join("")
      .toUpperCase() || "?"
  );
};

export function formatCurrency(
  amount: number,
  opts?: {
    currency?: string;
    locale?: string;
    minimumFractionDigits?: number;
    maximumFractionDigits?: number;
    noDecimals?: boolean;
  },
) {
  const { currency = "USD", locale = "en-US", minimumFractionDigits, maximumFractionDigits, noDecimals } = opts ?? {};

  const formatOptions: Intl.NumberFormatOptions = {
    style: "currency",
    currency,
    minimumFractionDigits: noDecimals ? 0 : minimumFractionDigits,
    maximumFractionDigits: noDecimals ? 0 : maximumFractionDigits,
  };

  return new Intl.NumberFormat(locale, formatOptions).format(amount);
}
