<!--
Relyper-owned: the original product concept by the project owner, kept
verbatim as the source specification. Do not edit the text below; record
deviations and status in ROADMAP.md instead.
-->

# Relyper Backup – Project Initialization and Implementation

## 1. Project overview

Create a new self-hosted, AI-native backup management platform called Relyper Backup, based on the open-source Backrest project.
Upstream repository:
https://github.com/garethgeorge/backrest
Documentation:
https://garethgeorge.github.io/backrest/
Backup engine:
https://github.com/restic/restic
Relyper Backup is part of the Relyper ecosystem.
The objective is to transform Backrest into an intelligent backup management platform with native MCP integration, automated Docker discovery, Easypanel integration, simplified configuration, reliable disaster recovery, and comprehensive backup monitoring.
This is not intended to be a simple external wrapper around Backrest. Relyper Backup should be an independent, maintainable downstream fork with its own branding and additional functionality.

## 2. Repository initialization

The current repository is the destination repository for Relyper Backup.

* Inspect the current repository before making changes.
* Import the complete upstream Backrest source code.
* Preserve upstream Git history wherever possible.
* Configure the original Backrest repository as an upstream Git remote.
* Preserve existing license files, copyright notices, and required upstream attribution.
* Verify and comply with all applicable licenses.
* Use the latest suitable stable upstream release as the initial foundation.
* Record the upstream version and commit used.
* Establish a maintainable strategy for integrating future upstream updates.

Do not rewrite the existing backup engine or replace working upstream functionality unnecessarily.

## 3. Core architectural principles

* Preserve Restic as the underlying backup engine.
* Preserve compatibility with standard Restic repositories.
* Maintain compatibility with existing Backrest functionality wherever practical.
* Extend the existing Go backend and frontend architecture.
* Prefer native backend integration over unnecessary additional services.
* Keep custom functionality modular to simplify future upstream merges.
* Use a single Docker image as the primary deployment method.
* Support deployment through Easypanel and standard Docker environments.
* Avoid unnecessary external dependencies.
* Do not introduce mandatory cloud services or proprietary dependencies.
* All core backup functionality must work without AI.

AI integration must enhance the product but must never become a dependency for successful backups or recovery.

## 4. Product branding and user interface

Rebrand the user-facing product as Relyper Backup.
The interface should be modern, clean, responsive, and suitable for both desktop and mobile devices.
Provide consistent light and dark themes.
Do not remove existing functionality merely to simplify the interface.
The main navigation should eventually include:

* Dashboard
* Applications
* Backup Plans
* Repositories
* Snapshots
* Restore
* Activity
* MCP Integration
* Settings

Preserve upstream attribution in an appropriate About or Licenses section.

## 5. Native MCP server

Implement a native Model Context Protocol server integrated into the Relyper Backup backend.
Use the official MCP SDK or another maintained, standards-compliant implementation compatible with the existing Go architecture.
Support:

* Streamable HTTP transport
* Authenticated remote connections
* MCP tool discovery
* Structured tool responses
* Input validation
* Permission enforcement
* Audit logging
* Connection management
* Secure credential handling
* Configurable read-only and administrative access

Evaluate current MCP specifications and supported client authentication mechanisms before implementation.
The MCP server must be compatible with external AI clients where supported, including ChatGPT, Claude, and other MCP-compatible applications.
Do not require users to install a separate MCP container.

### Read-only tools

Implement tools covering at least:

* get_system_overview
* get_backup_status
* list_repositories
* get_repository_status
* list_backup_plans
* get_backup_plan
* list_snapshots
* get_snapshot_details
* list_snapshot_files
* list_backup_operations
* get_backup_operation
* get_backup_errors
* get_storage_usage
* list_docker_containers
* list_docker_services
* list_docker_volumes
* list_easypanel_projects
* list_detected_databases
* get_backup_coverage
* get_disaster_recovery_status

### Administrative tools

Implement tools covering at least:

* test_repository_connection
* create_repository
* update_repository
* create_backup_plan
* update_backup_plan
* enable_backup_plan
* disable_backup_plan
* run_backup
* run_database_backup
* run_repository_check
* cancel_backup_operation
* create_backup_recommendations
* validate_backup_configuration
* run_restore_test

### Restricted operations

The following operations must require additional explicit authorization:

* restore_snapshot
* overwrite_existing_files
* delete_snapshot
* delete_repository
* prune_repository
* modify_retention_policy
* stop_container
* restart_container

Implement a server-side approval mechanism for sensitive operations.
Do not rely solely on instructions provided to the AI client.
Destructive operations should support a preview or dry-run mode wherever possible. Approval must be bound to the exact operation and parameters, expire automatically, and be auditable.
Never expose passwords, repository encryption keys, access tokens, private SSH keys, or other secrets through MCP responses.
Do not expose unrestricted shell execution through MCP.
Treat retrieved file contents, application metadata, logs, and other external data as untrusted input.

## 6. Docker discovery

Implement automatic discovery of the Docker environment.
Support:

* Running and stopped containers
* Docker Swarm services
* Docker Compose projects
* Container images and image tags
* Named volumes
* Bind mounts
* Networks
* Container labels
* Container restart policies
* Resource configurations
* Application dependencies

Use the Docker API instead of parsing CLI output wherever appropriate.
Identify which files and directories belong to individual applications.
Automatically classify application data into:

* Persistent application data
* Application configuration
* Database data
* Temporary data
* Cache data
* Reproducible container images
* Application deployment definitions

Docker integration must be optional.
Support least-privilege deployment and restricted Docker API access where practical.
Document that unrestricted access to the Docker socket is effectively host-level administrative access, even when the socket is mounted read-only.
Do not require unrestricted Docker access for ordinary file backups.

## 7. Easypanel integration

Easypanel is the primary initial deployment target.
Automatically discover Easypanel projects and services through supported APIs, Docker metadata, and verified filesystem structures.
Inspect the actual Easypanel implementation and environment before assuming storage paths or configuration formats.
Identify:

* Easypanel projects
* Application services
* Database services
* Persistent directories
* Docker volumes
* Bind mounts
* Domains
* Routing configuration
* Deployment definitions
* Environment variables
* Relevant configuration files
* Certificates where accessible and appropriate

Provide application-level backup configuration.
Users should be able to select an entire Easypanel application or project instead of manually identifying individual filesystem paths.
For example:
"Back up all applications belonging to the Relyper Budget project."
Relyper Backup should identify the relevant configuration, database services, persistent files, and dependencies.
Clearly report any inaccessible data or unsupported configuration.
Never report an application as fully protected if required components cannot be backed up.

## 8. Database discovery and consistent backups

Automatically detect supported database services.
Initial database support should include:

* PostgreSQL
* MySQL
* MariaDB
* Redis

Design the integration to support additional database engines in the future.
Implement database-specific, consistent backup strategies.
Use appropriate native database backup tools and mechanisms, such as pg_dump, pg_dumpall, mysqldump, mariadb-dump, and supported Redis persistence or snapshot mechanisms.
Do not assume that copying the live database filesystem produces a recoverable database backup.
Support:

* Automatic database discovery
* Database connection validation
* Individual database backups
* Complete database server backups where appropriate
* Backup compression
* Secure credential handling
* Temporary staging directories
* Automatic cleanup
* Backup verification
* Database restore testing
* Database version compatibility reporting

Do not interrupt production services by default.
Operations requiring downtime or service interruption must be clearly identified and explicitly authorized.
Failed database dumps must not be silently treated as successful application backups.

## 9. Backup coverage analysis

Introduce a dedicated Backup Coverage dashboard.
Automatically correlate discovered applications, services, databases, volumes, and configuration files with existing backup plans.
Every application should have a clearly understandable protection status:

* Fully Protected
* Partially Protected
* Not Protected
* Backup Failed
* Backup Overdue
* Restore Not Verified
* Unsupported Configuration

Display missing backup components and recommended corrective actions.
Support filtering by project, application, database type, protection status, and last successful backup.
Expose coverage information through MCP.
Example AI request:
"Which applications are currently not adequately protected, and what needs to be configured?"
Coverage must be based on actual configured and completed backups, not merely the existence of a backup plan.

## 10. Storage repositories

Preserve existing Backrest and Restic repository support.
Support storage backends available through Restic and maintained integrations, including:

* Local storage
* SFTP
* S3-compatible object storage
* Backblaze B2
* Cloudflare R2
* Azure Blob Storage
* Google Cloud Storage
* Rclone-supported storage backends
* Google Drive through an appropriate supported integration
* Dropbox through an appropriate supported integration

Do not claim native support for a provider unless the implementation actually supports it.
Implement a simplified repository setup wizard.
The wizard should provide:

* Storage provider selection
* Connection configuration
* Authentication setup
* Encryption password configuration
* Connection testing
* Remote directory validation
* Write permission testing
* Repository initialization
* Existing repository detection
* Storage usage information
* Actionable diagnostic messages

### Initial SFTP use case

The first real-world deployment will use an existing Host Europe hosting account providing approximately 371 GB of available external storage.
Support:

* SSH public key authentication
* Password authentication through secure supported mechanisms
* SSH host key verification
* Explicit host key trust management
* Persistent known_hosts configuration
* Configurable SSH port
* Remote directory selection
* Connection timeouts
* Detailed authentication and connection diagnostics

Never disable SSH host key verification as a default workaround.
Provide meaningful errors that distinguish DNS failures, connection failures, authentication failures, host key verification failures, incompatible algorithms, filesystem permissions, and repository errors.
Do not hardcode deployment-specific credentials or hostnames.

## 11. Simplified backup configuration

Implement an intelligent backup setup wizard.
Users should be able to select:

* Entire Docker environment
* Entire Easypanel environment
* Individual projects
* Individual applications
* Individual databases
* Individual directories
* Custom backup selections

The wizard should propose suitable backup plans based on discovered components.
Support configurable:

* Backup frequency
* Retention policies
* Daily, weekly, monthly, and yearly retention
* Compression
* Exclusion patterns
* Bandwidth limits
* Concurrent operations
* Repository maintenance
* Integrity checks
* Restore verification

Provide sensible defaults but allow advanced configuration.
Default suggestions may include:

* Critical databases every six hours
* Application files daily
* Deployment configuration daily
* Seven daily backups
* Four weekly backups
* Twelve monthly backups

These must remain configurable recommendations, not hardcoded requirements.
Show the estimated impact on storage and system resources where reliable information is available.

## 12. Dashboard and monitoring

Create a central dashboard providing:

* Overall backup health
* Number of protected applications
* Number of unprotected applications
* Last successful backup
* Next scheduled backup
* Failed backup operations
* Running backup operations
* Repository usage
* Available storage where detectable
* Backup duration
* Data transferred
* Storage efficiency
* Deduplication statistics
* Last successful integrity check
* Last verified restore

Distinguish estimated information from verified information.
Provide actionable error descriptions instead of generic connection or operation errors.
Support notification integrations for successful backups, failures, overdue backups, low storage, failed integrity checks, and failed restore tests.
Evaluate email, webhooks, and existing upstream notification capabilities before adding new implementations.

## 13. Disaster recovery

Implement disaster recovery as a first-class feature.
Relyper Backup must support recovery scenarios in which the original Docker host, Easypanel installation, and Relyper Backup container are completely unavailable.
Provide recovery workflows for:

* Individual files
* Individual application directories
* Individual Docker volumes
* Individual databases
* Complete applications
* Complete projects
* Easypanel configuration
* Migration to another Docker host

Generate a structured disaster recovery inventory containing:

* Application deployment definitions
* Required container images and versions
* Persistent storage mappings
* Network dependencies
* Database dependencies
* Relevant configuration files
* Required restoration order
* Host prerequisites
* Recovery instructions

Handle Docker Secrets and other non-exportable credentials explicitly. Do not claim they can be recovered unless their original source has been securely preserved.
Allow users to restore data into a separate staging directory before replacing production data.
Implement a recovery bootstrap mechanism independent of the original Relyper Backup installation.
Standard Restic repositories must remain recoverable using the ordinary Restic CLI.
Provide documented procedures for securely retaining repository access credentials and encryption passwords outside the protected host.
Loss of the original server must not automatically result in loss of the backup decryption credentials.

## 14. Backup verification

A successful backup operation is not sufficient proof of recoverability.
Implement or extend:

* Repository integrity checks
* Periodic snapshot validation
* Test restores
* File comparison
* Checksum validation where appropriate
* Database restore validation
* Recovery readiness reporting

Perform test restores in isolated locations or environments.
Never overwrite production files during automated restore verification.
Clearly distinguish:

* Backup completed
* Repository integrity verified
* Files successfully restored
* Database restore verified
* Application recovery verified

## 15. Security architecture

Relyper Backup will have access to highly sensitive infrastructure and application data.
Security is a core product requirement.
Implement:

* Secure authentication
* Role-based access control
* Least-privilege access
* Secure session handling
* API authorization
* MCP authorization
* Audit logging
* Secret redaction
* Secure filesystem permissions
* Secure credential storage
* TLS support
* Protection against path traversal
* Protection against command injection
* Protection against unauthorized filesystem access
* Protection against malicious restore paths
* Protection against unauthorized destructive actions

Provide separate permissions for:

* Viewing backup status
* Managing backup plans
* Managing storage repositories
* Running backups
* Browsing backup contents
* Restoring data
* Deleting backup data
* Managing infrastructure integrations

Reading backup contents must be treated as a sensitive permission.
Do not automatically send backed-up files, database contents, environment variables, or other confidential information to external AI providers.
Store repository passwords and storage credentials securely.
Document the threat model and residual risks, including the risk that a compromised backup-management server could delete accessible remote backups.
Evaluate immutable or append-only repository options where supported.

## 16. Relyper identity integration

Design authentication in a modular manner.
Support existing local authentication initially.
Prepare optional OpenID Connect integration for the Relyper Identity Provider.
Future identity integration should support:

* Sign in with Relyper
* External identity providers
* Role mapping
* Administrative access policies
* OAuth-based MCP authorization where supported

Do not make the Relyper Identity Provider mandatory for self-hosted installations.
Do not invent identity-provider endpoints or credentials.

## 17. Docker and Easypanel deployment

Provide a production-ready Docker image.
Support:

* Docker Compose deployment
* Easypanel deployment
* Persistent application configuration
* Persistent repository configuration
* Persistent SSH configuration
* Persistent cache
* Health checks
* Graceful shutdown
* Environment-based configuration
* Configurable web port
* Configurable MCP endpoint

Document required host mounts.
Separate read-only source mounts from writable application state, temporary database dump locations, and restore staging directories.
Do not mount the entire host filesystem with unrestricted write access.
Prevent recursive backup scenarios involving:

* Backup caches
* Temporary staging directories
* Restore directories
* Local backup repositories
* Relyper Backup's own transient files

Ensure that persistent Relyper Backup configuration can itself be backed up and restored securely.
Document security implications of every privileged deployment option.

## 18. Upstream maintenance

Maintain compatibility with upstream Backrest wherever practical.
Create documentation describing:

* Upstream repository configuration
* Current upstream baseline
* Relyper-specific modifications
* Upstream update procedure
* Merge and conflict resolution strategy
* Automated regression testing
* Dependency updates
* Database or configuration migrations

Prefer modular additions over widespread changes to upstream code.
Create automated tests that protect critical backup and restore behavior during future upstream updates.
AI-assisted maintenance must not bypass code review, security validation, or restore verification.

## 19. Testing and quality assurance

Implement appropriate automated tests for:

* Existing upstream functionality
* MCP tool registration
* MCP authentication
* MCP authorization
* Restricted operation approvals
* Docker discovery
* Easypanel discovery
* Database detection
* Database dumps
* Repository connection testing
* SFTP host key verification
* Backup execution
* Retention policy behavior
* Repository integrity
* File restoration
* Database restoration
* Secret redaction
* Error handling

Use isolated test environments and disposable repositories.
Never run destructive tests against production infrastructure.
Run available upstream tests before and after modifications.
Do not report functionality as completed unless it has actually been implemented and tested.
Clearly document any untested or partially supported functionality.

## 20. Documentation

Create and maintain:

* README.md
* AGENTS.md
* ARCHITECTURE.md
* ROADMAP.md
* SECURITY.md
* DEPLOYMENT.md
* DISASTER-RECOVERY.md
* MCP.md
* UPSTREAM.md
* CHANGELOG.md

Document development, deployment, configuration, MCP integration, backup procedures, recovery procedures, and known limitations.
All source code, comments, configuration examples, technical documentation, and command examples must be written in English.

## 21. Implementation priorities

### Phase 1 – Foundation

* Import and analyze upstream Backrest.
* Establish the upstream maintenance strategy.
* Verify successful local builds.
* Run existing tests.
* Introduce Relyper Backup branding.
* Establish Docker deployment.
* Improve SFTP configuration and diagnostics.
* Create architecture documentation and implementation roadmap.

### Phase 2 – Native MCP

* Implement the native MCP server.
* Implement authentication and authorization.
* Implement read-only management tools.
* Implement administrative backup tools.
* Implement secure approval workflows for restricted operations.
* Add MCP configuration and status to the web interface.

### Phase 3 – Infrastructure discovery

* Implement Docker discovery.
* Implement Docker Swarm support.
* Implement Easypanel discovery.
* Implement application-level backup selection.
* Implement backup coverage analysis.

### Phase 4 – Database protection

* Implement database discovery.
* Implement consistent database backups.
* Implement application-aware backup workflows.
* Implement database restoration and verification.

### Phase 5 – Disaster recovery

* Implement recovery inventories.
* Implement guided restoration workflows.
* Implement automated restore testing.
* Implement recovery readiness monitoring.

### Phase 6 – Product refinement

* Simplify repository and backup configuration.
* Refine dashboard and user experience.
* Implement optional Relyper identity integration.
* Extend notifications.
* Improve documentation and deployment templates.

## 22. Execution instructions

Start by inspecting the repository and obtaining the upstream source code.
Analyze the actual upstream architecture, APIs, storage mechanisms, authentication, frontend, Docker configuration, and licensing.
Create a practical implementation roadmap based on the findings.
Do not stop after producing a conceptual proposal.
Begin implementing Phase 1 immediately.
Build and test each meaningful change before proceeding.
Preserve working upstream functionality.
Do not replace reliable implementations with speculative abstractions.
Do not create placeholder features, fake status indicators, or mock production functionality.
When a feature cannot be implemented reliably, document the limitation and continue with other independent work.
Use incremental, reviewable commits.
Maintain an accurate record of completed work, remaining work, architectural decisions, and known issues.
The final product should make it possible for a user to connect external backup storage, discover an entire Docker or Easypanel environment, and configure reliable application-aware backups through either the web interface or an authorized AI agent.
The fundamental product promise is:
Relyper Backup – Intelligent backup management. Your infrastructure, your data, your control.
