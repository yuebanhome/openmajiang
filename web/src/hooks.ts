import { useCallback, useEffect, useRef, useState } from "react";
import { api, errorMessage } from "./api";
export function useRoute() {
  const [url, setURL] = useState(() => location.pathname + location.search);
  useEffect(() => {
    const update = () => setURL(location.pathname + location.search);
    window.addEventListener("popstate", update);
    return () => window.removeEventListener("popstate", update);
  }, []);
  return url;
}
export function navigate(path: string, replace = false) {
  if (!path.startsWith("/") || path.startsWith("//")) path = "/";
  history[replace ? "replaceState" : "pushState"]({}, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
  window.scrollTo?.(0, 0);
}
export function useResource<T>(path: string | null, interval = 0) {
  const [state, setState] = useState<{
    data?: T;
    loading: boolean;
    error: string;
  }>({ loading: !!path, error: "" });
  const [nonce, setNonce] = useState(0);
  const ref = useRef(0);
  const reload = useCallback(() => setNonce((v) => v + 1), []);
  useEffect(() => {
    const generation = ++ref.current;
    let controller: AbortController | undefined;
    let active = true;
    setState({ loading: !!path, error: "" });
    if (!path) return;
    const load = async () => {
      controller = new AbortController();
      try {
        const data = await api<T>(path, { signal: controller.signal });
        if (active && generation === ref.current)
          setState({ data, loading: false, error: "" });
      } catch (e) {
        if (
          active &&
          generation === ref.current &&
          !(e instanceof DOMException && e.name === "AbortError")
        )
          setState((s) => ({ ...s, loading: false, error: errorMessage(e) }));
      }
    };
    void load();
    const timer = interval
      ? window.setInterval(() => {
          if (document.visibilityState === "visible") void load();
        }, interval)
      : undefined;
    return () => {
      active = false;
      controller?.abort();
      if (timer) clearInterval(timer);
    };
  }, [path, interval, nonce]);
  return { ...state, reload };
}
