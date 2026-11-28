// relyper: user menu at the top right. It shows who is signed in, how
// (Relyper Identity or a local emergency user), the tenant and what the
// role allows, and holds the sign-out. Used by the operator app and the
// customer portal. The server enforces the rights; the menu only explains
// them.
import React, { useEffect, useState } from "react";
import { Badge, Box, Button, Flex, Stack, Text } from "@chakra-ui/react";
import {
  MenuContent,
  MenuItem,
  MenuRoot,
  MenuSeparator,
  MenuTrigger,
} from "../../components/ui/menu";
import { backendUrl } from "../../state/buildcfg";

export type Me = {
  method: "relyper" | "local" | "none";
  user?: string;
  name?: string;
  email?: string;
  roles?: string[];
  tenant?: string;
  provider_name?: string;
  admin: boolean;
  read_only: boolean;
  provider: boolean;
  provider_enabled?: boolean;
};

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

export type UserMenuRow = { label: string; value: string };

export type UserMenuViewProps = {
  // Shown on the trigger and as the menu title.
  title: string;
  subtitle?: string;
  badge?: { label: string; color: string; hint?: string };
  rows: UserMenuRow[];
  // What the role allows, in plain words.
  rights: string;
  onSignOut?: () => void;
  // The operator header is dark; the portal header is light.
  onDark?: boolean;
};

const initials = (s: string) => {
  const parts = s
    .replace(/@.*/, "")
    .split(/[\s._-]+/)
    .filter(Boolean);
  const v = (parts[0]?.[0] || "") + (parts[1]?.[0] || "");
  return (v || s[0] || "?").toUpperCase();
};

export const UserMenuView = ({
  title,
  subtitle,
  badge,
  rows,
  rights,
  onSignOut,
  onDark,
}: UserMenuViewProps) => (
  <MenuRoot positioning={{ placement: "bottom-end" }}>
    <MenuTrigger asChild>
      <Button
        variant="ghost"
        size="sm"
        px={2}
        color={onDark ? "white" : undefined}
        _hover={onDark ? { bg: "whiteAlpha.200" } : undefined}
        aria-label={`Signed in as ${title}`}
        data-testid="user-menu"
      >
        <Flex
          align="center"
          justify="center"
          w="26px"
          h="26px"
          borderRadius="full"
          bg={onDark ? "whiteAlpha.300" : "blue.600"}
          color="white"
          fontSize="xs"
          fontWeight="bold"
          flexShrink={0}
        >
          {initials(title)}
        </Flex>
        <Text
          as="span"
          maxW="180px"
          truncate
          display={{ base: "none", md: "inline" }}
        >
          {title}
        </Text>
        {badge && (
          <Badge
            colorPalette={badge.color}
            title={badge.hint}
            data-testid="user-menu-badge"
          >
            {badge.label}
          </Badge>
        )}
      </Button>
    </MenuTrigger>
    <MenuContent minW="260px" maxW="min(340px, calc(100vw - 24px))">
      <Box px={2} py={2} data-testid="user-menu-details">
        <Text fontWeight="semibold" wordBreak="break-word">
          {title}
        </Text>
        {subtitle && (
          <Text fontSize="xs" color="fg.muted" wordBreak="break-all">
            {subtitle}
          </Text>
        )}
        <Stack gap={1} mt={3} fontSize="xs">
          {rows.map((r) => (
            <Flex key={r.label} gap={2}>
              <Text color="fg.muted" minW="72px" flexShrink={0}>
                {r.label}
              </Text>
              <Text wordBreak="break-word">{r.value}</Text>
            </Flex>
          ))}
        </Stack>
        <Text fontSize="xs" mt={3} color="fg.muted">
          {rights}
        </Text>
      </Box>
      {onSignOut && (
        <>
          <MenuSeparator />
          <MenuItem
            value="sign-out"
            onClick={onSignOut}
            data-testid="user-menu-sign-out"
          >
            Sign out
          </MenuItem>
        </>
      )}
    </MenuContent>
  </MenuRoot>
);

// operatorView turns the server's description into menu content.
export const operatorView = (me: Me): Omit<UserMenuViewProps, "onSignOut"> => {
  if (me.method === "none") {
    return {
      title: "No sign-in",
      rows: [{ label: "Sign-in", value: "Authentication is turned off" }],
      rights:
        "Everyone who can reach this page has full access. Turn on authentication in the settings.",
      badge: { label: "Open", color: "red" },
    };
  }
  if (me.method === "local") {
    const emergency = !!me.provider_enabled;
    return {
      title: me.name || me.user || "Local user",
      rows: [
        {
          label: "Sign-in",
          value: emergency
            ? "Local user (emergency access)"
            : "Local user name and password",
        },
        {
          label: "Role",
          value: "Administrator (local users have full rights)",
        },
      ],
      rights: emergency
        ? "Full access. Use this account only when Relyper Identity is unavailable; normal work should use Sign in with Relyper."
        : "Full access: plans, repositories, backups, restores and settings.",
      badge: emergency
        ? {
            label: "Emergency",
            color: "orange",
            hint: "Signed in with a local emergency user",
          }
        : undefined,
    };
  }
  const provider = me.provider_name || "Relyper";
  const rows: UserMenuRow[] = [
    { label: "Sign-in", value: provider },
    {
      label: "Role",
      value: me.admin ? "Administrator" : "Read-only",
    },
  ];
  if (me.tenant) rows.push({ label: "Tenant", value: me.tenant });
  if (me.roles?.length)
    rows.push({ label: "Roles", value: me.roles.join(", ") });
  return {
    title: me.name || me.email || me.user || "Signed in",
    subtitle: me.name && me.email ? me.email : undefined,
    rows,
    rights: me.admin
      ? "Full access: plans, repositories, backups, restores, settings and approving MCP requests."
      : "You can view plans, repositories, snapshots, operations and logs. Changes, backups and restores are refused; ask an administrator for the admin role.",
    badge: me.read_only
      ? {
          label: "Read-only",
          color: "orange",
          hint: "Your role allows viewing only.",
        }
      : undefined,
  };
};

// UserMenu is the operator app's menu. It renders nothing until the server
// has answered; // fallback is shown when the server cannot describe the user.
export const UserMenu = ({
  onSignOut,
  fallback,
}: {
  onSignOut: () => void;
  fallback?: React.ReactNode;
}) => {
  const [me, setMe] = useState<Me | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    fetchMe()
      .then(setMe)
      .catch(() => setFailed(true));
  }, []);
  if (!me) return failed ? <>{fallback}</> : null;
  return (
    <UserMenuView
      {...operatorView(me)}
      onSignOut={me.method === "none" ? undefined : onSignOut}
      onDark
    />
  );
};

export default UserMenu;
