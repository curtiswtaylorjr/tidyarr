// The authed app shell. Past auth it renders the client-side router; the
// landing view is the read-only Discover browse (Stage 1 Wave 3). Later waves
// add the remaining views (Settings, workflows) and Discover's auto-grab. The
// router must never claim an /api/* path (see APP_ROUTES).

import { type Component, createSignal, Show } from "solid-js";
import { Route, Router } from "@solidjs/router";
import { Button, ErrorText, Muted } from "../components/ui";
import { Discover } from "./Discover";

// APP_ROUTES is the exhaustive list of client-side route patterns the router
// serves. Guardrail #2 / requirement #7: the router must NEVER claim any
// /api/* path (the OIDC callback /api/auth/oidc/callback is a real server
// route). A unit test asserts none of these start with "/api".
export const APP_ROUTES = ["/", "/discover"] as const;

const NotFound: Component = () => (
  <div class="rounded-xl border border-border bg-surface p-6">
    <h1 class="text-xl font-semibold text-fg">Not found</h1>
    <Muted class="mt-2">No such view. This is the SPA catch-all fallback.</Muted>
  </div>
);

export const AppShell: Component<{
  noneMode: boolean;
  connectionsSetupPending: boolean;
  onLoggedOut: () => void;
}> = (props) => {
  const [logoutError, setLogoutError] = createSignal("");

  const logout = async () => {
    setLogoutError("");
    try {
      await fetch("/api/auth/logout", { method: "POST" });
      props.onLoggedOut();
    } catch (err) {
      setLogoutError((err as Error).message);
    }
  };

  return (
    <div class="min-h-screen">
      <header class="flex items-center gap-4 border-b border-border bg-surface px-6 py-3">
        <span class="font-semibold text-fg">SAK Media Server</span>
        <div class="ml-auto">
          <Button onClick={logout}>Log out</Button>
        </div>
      </header>

      <Show when={props.noneMode}>
        <div class="border-b border-border bg-surface-2 px-6 py-2">
          <span class="text-sm text-danger">
            Authentication is disabled for this instance — it and every connected
            service is reachable by anyone who can reach it. Switch to a different
            mode in Settings to fix this.
          </span>
        </div>
      </Show>

      <Show when={props.connectionsSetupPending}>
        <div class="border-b border-border bg-surface-2 px-6 py-2">
          <span class="text-sm text-muted">
            First-run connections setup hasn't been dismissed yet — the setup
            wizard lands in a later wave.
          </span>
        </div>
      </Show>

      <main class="p-6">
        {logoutError() && <ErrorText>{logoutError()}</ErrorText>}
        <Router>
          <Route path="/" component={Discover} />
          <Route path="/discover" component={Discover} />
          <Route path="*" component={NotFound} />
        </Router>
      </main>
    </div>
  );
};
