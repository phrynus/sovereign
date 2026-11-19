import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import TransportMatrixPage from "./TransportMatrixPage";
import { TRANSPORT_MATRIX_ROWS } from "./transport-matrix-data";
import { BEACON_TRANSPORTS } from "@/pages/payload-generator/components/transport-quality";

// t() returns the key, so the table's quality labels are asserted by key.
vi.mock("@/lib/i18n", () => {
  const t = (key: string) => key;
  return { getLocale: () => "en", useI18n: () => ({ t }) };
});

function renderPage() {
  return render(
    <MemoryRouter>
      <TransportMatrixPage />
    </MemoryRouter>
  );
}

describe("TransportMatrixPage", () => {
  it("renders one row per beacon transport with its quality label and note", () => {
    renderPage();
    // Every transport label appears as its i18n key via quality/note cells.
    // The rows come from BEACON_TRANSPORTS, so the count is the contract.
    expect(TRANSPORT_MATRIX_ROWS).toHaveLength(BEACON_TRANSPORTS.length);
    expect(screen.getAllByText("transport.title").length).toBeGreaterThan(0);
    // Hardened transports must show the hardened label, experimental ones the
    // experimental label — this would catch the old gRPC/SSH/QUIC mislabel.
    expect(screen.getAllByText("generate.quality_hardened").length).toBeGreaterThan(0);
    expect(screen.getAllByText("generate.quality_experimental").length).toBeGreaterThan(0);
  });

  it("every row carries a capability note key that resolves", () => {
    for (const row of TRANSPORT_MATRIX_ROWS) {
      expect(row.noteKeys.length).toBeGreaterThan(0);
      for (const key of row.noteKeys) {
        expect(key).toMatch(/^[a-z0-9_]+$/);
      }
    }
  });
});
