// relyper: guided recovery of one app. Step 1 restores the latest snapshot
// into a new staging directory (never on top of live data); step 2 is the
// runbook with the exact commands to put the data back on the Docker host
// in the right order. Replacing production data stays a manual step.
import React, { useEffect, useState } from "react";
import { Box, Code, Stack, Text } from "@chakra-ui/react";
import { Button } from "../../components/ui/button";
import { RecoveryGuide, StageResult, infraApi } from "./infraApi";

const errMsg = (e: any) => e?.message || String(e);

export const RecoveryPanel = ({ app }: { app: string }) => {
  const [guide, setGuide] = useState<RecoveryGuide | null>(null);
  const [stage, setStage] = useState<StageResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    infraApi
      .recoveryGuide(app, stage?.target)
      .then(setGuide)
      .catch((e) => setError(errMsg(e)));
  }, [app, stage?.target]);

  const doStage = async () => {
    setBusy(true);
    setError(null);
    try {
      setStage(await infraApi.stageRestore(app));
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Stack
      gap={3}
      fontSize="sm"
      borderWidth="1px"
      borderRadius="md"
      p={3}
      wordBreak="break-word"
      data-testid="recovery-panel"
    >
      <Text fontWeight="medium">Restore this app</Text>
      <Text color="fg.muted">
        First restore the latest snapshot into a new staging directory. Nothing
        in production changes. Then follow the steps below on the Docker host.
      </Text>
      <Box>
        <Button
          size="sm"
          variant="outline"
          loading={busy}
          onClick={doStage}
          disabled={!!stage}
          data-testid="stage-restore"
        >
          1. Restore to staging
        </Button>
      </Box>
      {stage && (
        <Text data-testid="stage-result">
          Restoring snapshot {stage.snapshot_id.slice(0, 8)} (
          {new Date(stage.snapshot_time).toLocaleString()}) of plan {stage.plan}{" "}
          into <Code>{stage.target}</Code>. Progress is in the operation list of
          the plan.
        </Text>
      )}
      {error && <Text color="red.500">{error}</Text>}
      {guide && (
        <Stack gap={2} data-testid="recovery-guide">
          <Text>
            Start order: <b>{guide.order.join(" → ")}</b>
          </Text>
          {guide.steps.map((s, i) => (
            <Box key={s.title}>
              <Text fontWeight="medium">
                {i + 2}. {s.title}
              </Text>
              {s.detail && <Text color="fg.muted">{s.detail}</Text>}
              {(s.commands || []).length > 0 && (
                <Code
                  display="block"
                  whiteSpace="pre-wrap"
                  wordBreak="break-all"
                  p={2}
                  mt={1}
                  fontSize="xs"
                >
                  {(s.commands || []).join("\n")}
                </Code>
              )}
            </Box>
          ))}
          {(guide.notes || []).map((n) => (
            <Text key={n} color="orange.500">
              {n}
            </Text>
          ))}
        </Stack>
      )}
    </Stack>
  );
};

export default RecoveryPanel;
