// useRowOrder — the row-order state machine Mainstream.tsx and Adult.tsx
// both drive: loads the screen's stored key order once, merges it against
// the screen's current known keys (mergeRowOrder), and persists a move
// immediately (no separate Save step, same per-click-persists convention as
// SliderAdmin/AdultRowAdmin). Extracted after both screens ended up with
// byte-for-byte identical load/merge/move/persist logic, parameterized only
// by screen name and knownKeys — see mergeRowOrder's own doc comment for why
// persistence itself stays deliberately loose (best-effort, not validated
// against a fixed id set the way rssfeeds.Store.Reorder is).

import { createEffect, createSignal } from "solid-js";
import {
  type DiscoverScreen,
  fetchRowOrder,
  mergeRowOrder,
  saveRowOrder,
} from "../../api/rowOrder";

export function useRowOrder(screen: DiscoverScreen, knownKeys: () => string[]) {
  const [storedKeys, setStoredKeys] = createSignal<string[] | null>(null);
  createEffect(() => {
    if (storedKeys() === null) {
      fetchRowOrder(screen)
        .then(setStoredKeys)
        .catch(() => setStoredKeys([]));
    }
  });

  const orderedKeys = () => mergeRowOrder(storedKeys() ?? [], knownKeys());

  const [error, setError] = createSignal("");
  const persistOrder = (keys: string[]) => {
    setStoredKeys(keys);
    void saveRowOrder(screen, keys).catch((e) => setError((e as Error).message));
  };

  const moveRow = (key: string, direction: -1 | 1) => {
    const keys = orderedKeys();
    const idx = keys.indexOf(key);
    const swapWith = idx + direction;
    if (idx < 0 || swapWith < 0 || swapWith >= keys.length) return;
    const next = [...keys];
    [next[idx], next[swapWith]] = [next[swapWith]!, next[idx]!];
    persistOrder(next);
  };

  return { orderedKeys, moveRow, persistOrder, error };
}
