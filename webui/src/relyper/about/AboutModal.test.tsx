import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWithProviders } from "../../test/render";
import { AboutModal } from "./AboutModal";

describe("AboutModal", () => {
  it("keeps the upstream attribution and license", async () => {
    renderWithProviders(<AboutModal />);
    const about = await screen.findByTestId("relyper-about");
    expect(about).toHaveTextContent("based on Backrest by Gareth George");
    expect(about).toHaveTextContent("GNU General Public License v3.0");
    expect(about).toHaveTextContent("restic");
    expect(screen.getByRole("link", { name: "Backrest" })).toHaveAttribute(
      "href",
      "https://github.com/garethgeorge/backrest",
    );
  });
});
