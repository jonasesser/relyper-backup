// relyper: tests for the report Markdown view.
import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { renderWithProviders } from "../../test/render";
import { Markdown } from "./Markdown";

describe("Markdown", () => {
  it("renders headings, lists, tables and code without raw HTML", () => {
    const md = [
      "# Disaster recovery report",
      "## If the whole server is lost",
      "1. **New server** with Docker.",
      "2. Apps:",
      "   - shop (`compose:shop`): plan `app-shop`",
      "| Repository | Location |",
      "|---|---|",
      "| `local` | `/repo` |",
      "```sh",
      "sh relyper-recover.sh plans",
      "```",
      "<img src=x onerror=alert(1)>",
    ].join("\n");
    const { container } = renderWithProviders(<Markdown text={md} />);
    expect(
      screen.getByRole("heading", { name: "If the whole server is lost" }),
    ).toBeInTheDocument();
    expect(container.querySelector("ol li b")).toHaveTextContent("New server");
    expect(container.querySelectorAll("ol > li")).toHaveLength(2);
    expect(container.querySelector("ol li ul li")).toHaveTextContent("shop");
    expect(container.querySelector("table")).toHaveTextContent("/repo");
    expect(container.querySelector("pre")).toHaveTextContent(
      "sh relyper-recover.sh plans",
    );
    expect(container.querySelector("img")).toBeNull();
    expect(container).toHaveTextContent("<img src=x onerror=alert(1)>");
  });
});
