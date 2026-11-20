import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { TooltipProvider } from "@/components/ui/tooltip";
import { DEFAULT_ONELINER_FORM } from "@/types/generate";
import OneLinerPanel from "./OneLinerPanel";

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  useI18n: () => ({ t: (key: string) => key }),
}));

function renderPanel(payloadType: string) {
  return render(
    <TooltipProvider delay={0}>
      <OneLinerPanel
        form={{ ...DEFAULT_ONELINER_FORM, payload_type: payloadType, listener_id: "1" }}
        setForm={() => {}}
        busy={false}
        result={null}
        listeners={[]}
        getListenerInfo={() => null}
        onGenerate={() => {}}
      />
    </TooltipProvider>,
  );
}

describe("OneLinerPanel PS1 delivery selectors", () => {
  it("shows obfuscation and PowerShell runtime selectors only for ps1 payloads", () => {
    const { unmount } = renderPanel("ps1");
    expect(screen.getByText("generate.oneliner_obfuscation")).toBeTruthy();
    expect(screen.getByText("generate.oneliner_runtime")).toBeTruthy();
    unmount();

    renderPanel("exe");
    expect(screen.queryByText("generate.oneliner_obfuscation")).toBeNull();
    expect(screen.queryByText("generate.oneliner_runtime")).toBeNull();
  });
});
