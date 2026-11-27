import { describe, expect, it } from "vitest";
// @ts-ignore - plain ESM build script without type declarations
import { brandMessages, brandText } from "../../relyper/brand-messages.mjs";

describe("brandMessages", () => {
  it("replaces the product name except for upstream references", () => {
    const out = brandMessages({
      $schema: "https://inlang.com/schema/inlang-message-format",
      a: "Backrest has detected that you do not have any users configured.",
      b: "Andere Backrest-Instanzen",
      dashboard_getting_started_overview_d_b: "the Backrest wiki",
    });
    expect(out.a).toBe(
      "Relyper Backup has detected that you do not have any users configured.",
    );
    expect(out.b).toBe("Andere Relyper Backup-Instanzen");
    expect(out.dashboard_getting_started_overview_d_b).toBe(
      "the Backrest wiki",
    );
    expect(out.$schema).toBe("https://inlang.com/schema/inlang-message-format");
  });
});

describe("brandText", () => {
  it.each([
    ["Initial backrest setup! ", "Initial Relyper Backup setup! "],
    [
      "identify this backrest install.",
      "identify this Relyper Backup install.",
    ],
    ["(e.g. my-backrest-server)", "(e.g. my-relyper-backup)"],
    [
      "Erste Einrichtung von backrest! ",
      "Erste Einrichtung von Relyper Backup! ",
    ],
    [
      "(typically in $HOME/.backrest/config.json)",
      "(typically in $HOME/.backrest/config.json)",
    ],
    [
      "(backrest must be running as root)",
      "(Relyper Backup must be running as root)",
    ],
  ])("%s", (input, want) => {
    expect(brandText(input)).toBe(want);
  });
});
