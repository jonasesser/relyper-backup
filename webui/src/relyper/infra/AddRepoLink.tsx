// relyper: "no repository yet" hint with a link that opens the dialog to
// add one. The surrounding form stays open and picks up the new repository.
import React from "react";
import { Link, Text } from "@chakra-ui/react";
import { useShowModal } from "../../components/common/ModalManager";

export const AddRepoLink = () => {
  const showModal = useShowModal();
  return (
    <Text color="red.500" data-testid="no-repo-hint">
      No repository yet.{" "}
      <Link
        as="button"
        color="blue.500"
        textDecoration="underline"
        data-testid="add-repo-link"
        onClick={async () => {
          const { AddRepoModal } =
            await import("../../features/repositories/AddRepoModal");
          showModal(<AddRepoModal template={null} />);
        }}
      >
        Add a repository
      </Link>{" "}
      first; this form stays open.
    </Text>
  );
};
