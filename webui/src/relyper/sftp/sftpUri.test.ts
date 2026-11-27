import { describe, expect, it } from "vitest";
import { parseResticSftpUri } from "./sftpUri";

describe("parseResticSftpUri", () => {
  it.each([
    [
      "sftp:user@host:/srv/restic",
      { user: "user", host: "host", path: "/srv/restic" },
    ],
    ["sftp:user@host:restic", { user: "user", host: "host", path: "restic" }],
    ["sftp:host:", { user: undefined, host: "host", path: "" }],
    [
      "sftp://user@host:2222//srv/restic",
      { user: "user", host: "host", port: 2222, path: "/srv/restic" },
    ],
    [
      "sftp://user@host/restic",
      { user: "user", host: "host", port: undefined, path: "restic" },
    ],
    [
      "sftp://u@[2001:db8::1]:22//r",
      { user: "u", host: "2001:db8::1", port: 22, path: "/r" },
    ],
    ["sftp:u@[2001:db8::1]:/r", { user: "u", host: "2001:db8::1", path: "/r" }],
  ])("parses %s", (uri, want) => {
    expect(parseResticSftpUri(uri)).toEqual(want);
  });

  it("rejects non-sftp and empty hosts", () => {
    expect(parseResticSftpUri("s3:bucket")).toBeNull();
    expect(parseResticSftpUri("sftp:")).toBeNull();
  });
});
