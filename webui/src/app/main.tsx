import * as React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { ModalContextProvider } from "../components/common/ModalManager";
import { Toaster } from "../components/ui/toaster";

import "react-js-cron/dist/styles.css";

import { HashRouter } from "react-router";
import { AppProvider } from "./provider";

// relyper: the customer portal (#/portal) is a separate screen without the operator UI.
const PortalApp = React.lazy(() => import("../relyper/portal/PortalApp"));
const isPortal = window.location.hash.startsWith("#/portal");

const Root = ({ children }: { children: React.ReactNode }) => {
  return (
    <AppProvider>
      <Toaster />
      <ModalContextProvider>{children}</ModalContextProvider>
    </AppProvider>
  );
};

const darkTheme = window.matchMedia("(prefers-color-scheme: dark)").matches;

const el = document.querySelector("#app");
el &&
  createRoot(el).render(
    <React.StrictMode>
      <Root>
        {isPortal ? (
          <React.Suspense fallback={null}>
            <PortalApp />
          </React.Suspense>
        ) : (
          <HashRouter>
            <App />
          </HashRouter>
        )}
      </Root>
    </React.StrictMode>,
  );
