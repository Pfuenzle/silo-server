export function authMappingInstallation(authorizationMode: "none" | "external_groups_v1") {
  return {
    id: 17,
    plugin_id: "silo.auth.ldap",
    version: "1.0.0",
    install_path: "/plugins/ldap",
    enabled: true,
    source_kind: "silo",
    updates_paused: false,
    capabilities: [
      {
        type: "auth_provider.v1",
        id: "ldap",
        display_name: "LDAP directory",
        description: "Authenticate against LDAP.",
      },
    ],
    global_config_schema: [],
    user_config_schema: [],
    routes: [],
    assets: [],
    global_configs: [],
    auth_bindings: [
      {
        capability_id: "ldap",
        enabled: true,
        display_order: 1,
        auto_provision: true,
        default_login: true,
        authorization_mode: authorizationMode,
        created_at: "2026-07-30T00:00:00Z",
        updated_at: "2026-07-30T00:00:00Z",
      },
    ],
    task_bindings: [],
    update_policy: "auto",
  };
}
