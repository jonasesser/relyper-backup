// relyper: header badge for users signed in through Relyper Identity with
// only the login role. The server enforces read-only access; this badge
// explains why changes are refused.
import React, { useEffect, useState } from "react";
import { Badge } from "@chakra-ui/react";
import { backendUrl } from "../../state/buildcfg";

export type Me = { read_only: boolean; provider: boolean; user?: string };

export const fetchMe = async (): Promise<Me> => {
  const h: Record<string, string> = {};
  const token = localStorage.getItem("backrest-ui-authToken");
  if (token) h["Authorization"] = "Bearer " + token;
  const resp = await window.fetch(
    new URL(
      "relyper/identity/me",
      new URL(backendUrl, window.location.href),
    ).toString(),
    { headers: h },
  );
  if (!resp.ok) throw new Error(`${resp.status}`);
  return resp.json();
};

export const ReadOnlyBadge = () => {
  const [me, setMe] = useState<Me | null>(null);
  useEffect(() => {
    fetchMe()
      .then(setMe)
      .catch(() => setMe(null));
  }, []);
  if (!me?.read_only) return null;
  return (
    <Badge
      colorPalette="orange"
      title="Your role allows viewing only. Ask an administrator for the admin role to change plans, repositories or settings and to start backups or restores."
      data-testid="read-only-badge"
    >
      Read-only
    </Badge>
  );
};

export default ReadOnlyBadge;
