import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { rpc } from "@/api/client";
import { queryClient } from "@/api/queryClient";
import { inTauri } from "@/api/transport";
import { router } from "@/app/router";
import { plainInput } from "@/lib/plainInput";
import "@/styles/app.css";

if (!inTauri) document.documentElement.classList.add("browser");
plainInput();

void rpc.start();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
