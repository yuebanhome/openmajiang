import { useEffect, useRef, useState } from "react";
import { api, errorMessage, listFrom } from "./api";
export function useCursorList<T>(path: string | null, key = "matches") {
  const generation = useRef(0);
  const fetching = useRef(false);
  const [state, setState] = useState<{
    path: string | null;
    items: T[];
    next: string;
    loading: boolean;
    error: string;
  }>({ path: null, items: [], next: "", loading: false, error: "" });
  useEffect(() => {
    const version = ++generation.current;
    const abort = new AbortController();
    setState({ path, items: [], next: "", loading: !!path, error: "" });
    if (!path) return;
    void api<Record<string, unknown>>(path, { signal: abort.signal })
      .then((data) => {
        if (version === generation.current)
          setState({
            path,
            items: listFrom<T>(data, key),
            next: typeof data.next_before === "string" ? data.next_before : "",
            loading: false,
            error: "",
          });
      })
      .catch((error) => {
        if (
          version === generation.current &&
          !(error instanceof DOMException && error.name === "AbortError")
        )
          setState({
            path,
            items: [],
            next: "",
            loading: false,
            error: errorMessage(error),
          });
      });
    return () => {
      generation.current++;
      abort.abort();
    };
  }, [path, key]);
  const more = async () => {
    if (
      !path ||
      state.path !== path ||
      !state.next ||
      state.loading ||
      fetching.current
    )
      return;
    fetching.current = true;
    const version = generation.current;
    const cursor = state.next;
    setState((s) => ({ ...s, loading: true, error: "" }));
    try {
      const data = await api<Record<string, unknown>>(
        `${path}${path.includes("?") ? "&" : "?"}before=${encodeURIComponent(cursor)}`,
      );
      if (version === generation.current)
        setState((s) => ({
          ...s,
          items: [...s.items, ...listFrom<T>(data, key)],
          next:
            typeof data.next_before === "string" && data.next_before !== cursor
              ? data.next_before
              : "",
          loading: false,
        }));
    } catch (error) {
      if (version === generation.current)
        setState((s) => ({ ...s, loading: false, error: errorMessage(error) }));
    } finally {
      fetching.current = false;
    }
  };
  return state.path === path
    ? { ...state, more }
    : { path, items: [] as T[], next: "", loading: !!path, error: "", more };
}
