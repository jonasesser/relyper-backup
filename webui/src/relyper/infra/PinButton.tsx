// relyper: pin or unpin a snapshot. A pinned snapshot (restic tag
// "relyper-pinned") is kept by every retention run and cannot be forgotten
// until it is unpinned. restic rewrites the snapshot, so it gets a new ID;
// the server refreshes the snapshot list afterwards.
import React, { useState } from "react";
import { Badge, Flex } from "@chakra-ui/react";
import { LuPin, LuPinOff } from "react-icons/lu";
import { Button } from "../../components/ui/button";
import { alerts } from "../../components/common/Alerts";
import { infraApi, PINNED_TAG } from "./infraApi";

export const isPinned = (tags?: string[]) => !!tags?.includes(PINNED_TAG);

export const PinButton = ({
  repo,
  snapshot,
  tags,
  onDone,
}: {
  repo: string;
  snapshot: string;
  tags?: string[];
  onDone?: () => void;
}) => {
  const pinned = isPinned(tags);
  const [busy, setBusy] = useState(false);
  const toggle = async () => {
    setBusy(true);
    try {
      await infraApi.setPinned(repo, snapshot, !pinned);
      alerts.success(
        pinned
          ? "Unpinned. Retention may remove this snapshot again."
          : "Pinned. This snapshot is kept until you unpin it. The list shows it with a new ID in a moment.",
      );
      onDone?.();
    } catch (e: any) {
      alerts.error(e?.message || String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Flex gap={2} align="center">
      {pinned && (
        <Badge
          colorPalette="purple"
          data-testid="pinned-badge"
          title="Kept until you unpin it; retention never removes it"
        >
          <LuPin /> Pinned
        </Badge>
      )}
      <Button
        size="xs"
        variant="outline"
        loading={busy}
        onClick={toggle}
        data-testid="pin-snapshot"
        title={
          pinned
            ? "Allow retention to remove this snapshot again"
            : "Keep this snapshot forever: retention never removes it"
        }
      >
        {pinned ? <LuPinOff /> : <LuPin />}
        {pinned ? "Unpin" : "Pin (keep forever)"}
      </Button>
    </Flex>
  );
};
