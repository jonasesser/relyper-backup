// relyper: a small Markdown view for the reports Relyper Backup writes
// itself (headings, lists, tables, code blocks, **bold**, `code`). It builds
// React elements, never raw HTML, so report content cannot inject markup.
import React from "react";
import { Box, Code, Heading, Table, Text } from "@chakra-ui/react";

const inline = (s: string): React.ReactNode[] => {
  const out: React.ReactNode[] = [];
  const re = /(\*\*[^*]+\*\*|`[^`]+`)/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s))) {
    if (m.index > last) out.push(s.slice(last, m.index));
    const t = m[0];
    out.push(
      t.startsWith("**") ? (
        <b key={m.index}>{t.slice(2, -2)}</b>
      ) : (
        <Code key={m.index} fontSize="0.9em" wordBreak="break-all">
          {t.slice(1, -1)}
        </Code>
      ),
    );
    last = m.index + t.length;
  }
  if (last < s.length) out.push(s.slice(last));
  return out;
};

const cells = (line: string) =>
  line
    .trim()
    .replace(/^\||\|$/g, "")
    .split("|")
    .map((c) => c.trim());

export const Markdown = ({ text }: { text: string }) => {
  const lines = text.split("\n");
  const blocks: React.ReactNode[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    const key = blocks.length;
    if (line.startsWith("```")) {
      const body: string[] = [];
      for (i++; i < lines.length && !lines[i].startsWith("```"); i++)
        body.push(lines[i]);
      i++;
      blocks.push(
        <Code
          key={key}
          as="pre"
          display="block"
          p={2}
          my={2}
          fontSize="xs"
          whiteSpace="pre-wrap"
          wordBreak="break-word"
        >
          {body.join("\n")}
        </Code>,
      );
      continue;
    }
    const h = /^(#{1,4}) (.*)$/.exec(line);
    if (h) {
      const size = ["xl", "lg", "md", "sm"][h[1].length - 1] as any;
      blocks.push(
        <Heading key={key} size={size} mt={h[1].length < 3 ? 5 : 3} mb={2}>
          {inline(h[2])}
        </Heading>,
      );
      i++;
      continue;
    }
    if (line.startsWith("|")) {
      const rows: string[][] = [];
      for (; i < lines.length && lines[i].startsWith("|"); i++) {
        if (!/^\|[\s|:-]+\|$/.test(lines[i].trim())) rows.push(cells(lines[i]));
      }
      const [head, ...body] = rows;
      blocks.push(
        <Box key={key} overflowX="auto" my={2}>
          <Table.Root size="sm">
            <Table.Header>
              <Table.Row>
                {head.map((c, j) => (
                  <Table.ColumnHeader key={j}>{inline(c)}</Table.ColumnHeader>
                ))}
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {body.map((r, k) => (
                <Table.Row key={k}>
                  {r.map((c, j) => (
                    <Table.Cell key={j} wordBreak="break-word">
                      {inline(c)}
                    </Table.Cell>
                  ))}
                </Table.Row>
              ))}
            </Table.Body>
          </Table.Root>
        </Box>,
      );
      continue;
    }
    const li = /^(\s*)(?:[-*]|\d+\.) (.*)$/.exec(line);
    if (li) {
      const ordered = /^\s*\d+\./.test(line);
      // Indented items become a list inside the item above them.
      const items: { text: string; sub: string[] }[] = [];
      for (; i < lines.length; i++) {
        const m = /^(\s*)(?:[-*]|\d+\.) (.*)$/.exec(lines[i]);
        if (!m) break;
        if (m[1].length && items.length) items[items.length - 1].sub.push(m[2]);
        else items.push({ text: m[2], sub: [] });
      }
      blocks.push(
        <Box
          key={key}
          as={ordered ? "ol" : "ul"}
          pl={5}
          my={2}
          listStyleType={ordered ? "decimal" : "disc"}
        >
          {items.map((it, k) => (
            <li key={k}>
              {inline(it.text)}
              {it.sub.length > 0 && (
                <Box as="ul" pl={5} listStyleType="disc">
                  {it.sub.map((x, n) => (
                    <li key={n}>{inline(x)}</li>
                  ))}
                </Box>
              )}
            </li>
          ))}
        </Box>,
      );
      continue;
    }
    if (line.startsWith("> ")) {
      blocks.push(
        <Text key={key} color="fg.muted" fontSize="sm" my={1}>
          {inline(line.slice(2))}
        </Text>,
      );
      i++;
      continue;
    }
    if (line.trim()) {
      blocks.push(
        <Text key={key} my={2}>
          {inline(line)}
        </Text>,
      );
    }
    i++;
  }
  return <Box fontSize="sm">{blocks}</Box>;
};
