// relyper: About dialog with product version and upstream attribution
// (required by the licenses of Backrest and restic, see NOTICE and LICENSE).
import React from "react";
import { Link, Stack, Text } from "@chakra-ui/react";
import { FormModal } from "../../components/common/FormModal";
import { useShowModal } from "../../components/common/ModalManager";
import { uiBuildVersion } from "../../state/buildcfg";
import { Button } from "../../components/ui/button";

export const RELYPER_REPO_URL = "https://github.com/jonasesser/relyper-backup";

export const AboutModal = () => {
  const showModal = useShowModal();
  return (
    <FormModal
      isOpen={true}
      onClose={() => showModal(null)}
      title="About Relyper Backup"
      footer={<Button onClick={() => showModal(null)}>Close</Button>}
    >
      <Stack gap={3} fontSize="sm" data-testid="relyper-about">
        <Text>
          <strong>Relyper Backup</strong> – Intelligent backup management. Your
          infrastructure, your data, your control.
        </Text>
        <Text>Version: {uiBuildVersion}</Text>
        <Text>
          Source code:{" "}
          <Link href={RELYPER_REPO_URL} target="_blank" rel="noreferrer">
            {RELYPER_REPO_URL}
          </Link>
        </Text>
        <Text>
          Relyper Backup is based on{" "}
          <Link
            href="https://github.com/garethgeorge/backrest"
            target="_blank"
            rel="noreferrer"
          >
            Backrest
          </Link>{" "}
          by Gareth George and contributors, licensed under the GNU General
          Public License v3.0. Relyper Backup is distributed under the same
          license.
        </Text>
        <Text>
          Backups are made by{" "}
          <Link
            href="https://github.com/restic/restic"
            target="_blank"
            rel="noreferrer"
          >
            restic
          </Link>{" "}
          (BSD 2-Clause License). Repositories stay standard restic repositories
          and can always be restored with the restic command line tool.
        </Text>
        <Text color="fg.muted">
          Relyper Backup is not affiliated with or endorsed by the Backrest or
          restic projects.
        </Text>
      </Stack>
    </FormModal>
  );
};
