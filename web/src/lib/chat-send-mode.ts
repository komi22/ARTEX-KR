"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// 세션 입력 상자의 보내기/줄 바꿈 키입니다. 순수 프런트 엔드 기본 설정: localStorage만, 저장소 없음, 계정과 동기화 없음,
//따라서 브라우저를 변경하려면 재설정이 필요합니다. 이슈 #39 참조 - 0.3.2에서는 Ctrl+Enter를 Enter 키로 보내도록 변경했습니다.
// 여기서 이전 키를 반환하는 것은 선택 사항입니다.
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

export const CHAT_SEND_MODE_OPTIONS: { value: ChatSendMode; label: string }[] = [
  { value: "enter", label: "보내려면 Enter를, 줄을 바꾸려면 Shift+Enter" },
  { value: "ctrl-enter", label: "보내려면 Ctrl+Enter, 줄을 바꾸려면 Enter" },
];

function parseMode(raw: string | null): ChatSendMode {
  return raw === "ctrl-enter" || raw === "enter" ? raw : DEFAULT_CHAT_SEND_MODE;
}

// 동일한 탭 내의 구독자 모음입니다. localStorage의 저장 이벤트는 "기타" 탭에서만 트리거됩니다.
// 설정에서 이 페이지를 변경한 후 동일한 페이지의 입력 상자에 알리려면 내보내야 하며, 그렇지 않으면 새로 고쳐야 적용됩니다.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// 반환되는 것은 문자열 리터럴이며 Object.is는 값으로 비교되며 useSyncExternalStore는 루프에 빠지지 않습니다.
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// 서버에 localStorage가 없으므로 기본값이 먼저 렌더링되고 수화되고 getSnapshot에 의해 수정됩니다.
function getServerSnapshot(): ChatSendMode {
  return DEFAULT_CHAT_SEND_MODE;
}

export function useChatSendMode(): ChatSendMode {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function setChatSendMode(mode: ChatSendMode) {
  setLocalStorageValue(CHAT_SEND_MODE_KEY, mode);
  for (const listener of listeners) listener();
}

// shouldSubmitOnKey는 키 입력을 보내야 하는지 여부를 결정합니다.
// isComposing / keyCode 229는 중국어 등의 입력 방법이 단어를 선택하므로 허용해야 함을 의미합니다. 그렇지 않으면 Enter를 눌러 단어 선택이 실수로 전송됩니다.
// 입력 모드에서는 0.3.2의 동작과 그대로 일치하는 Shift만 제외됩니다. 즉, 설정을 변경하지 않은 사용자 경험은 변경되지 않습니다.
// Ctrl-Enter 모드에서는 Ctrl과 Cmd(macOS)를 모두 허용합니다.
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
