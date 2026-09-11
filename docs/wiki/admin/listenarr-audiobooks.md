---
title: Listenarr audiobook requests
description: Install and configure the Listenarr request-router integration for audiobook requests.
summary: Plugin installation, encrypted configuration, path mapping, and request lifecycle behavior.
tags:
  - silo
  - admin
  - listenarr
  - audiobooks
audience:
  - operator
last_reviewed: 2026-09-09
related:
  - ../deployment/docker.md
  - media-folder-and-naming.md
---

# Listenarr audiobook requests

Silo can send approved audiobook requests to Listenarr through the `request_router.v1`
plugin capability. The plugin is optional: movie and series requests continue to use their
existing integrations, and Silo does not require a Listenarr URL at startup.

## Install the plugin

1. Open **Admin > Plugins**.
2. Install the Listenarr request-router plugin from the trusted plugin catalog or your
   approved plugin package source.
3. Confirm that the installation advertises the `request_router.v1` capability and audiobook
   support.
4. Open **Admin > Requests > Integrations**, choose the installed capability, and create a
   connection.

## Configure the connection

Enter the Listenarr base URL reachable from the Silo process, the plugin's required connection
options, and the API key in the encrypted secret field. The API key is never part of request
responses or the rendered request page. Keep it out of `.env` files, commits, screenshots, and
diagnostic reports.

The URL is deployment-specific. Configure the URL used by your deployment in the admin form.
For example:

```text
http://listenarr.example:4545
```

This is not a Silo default and must not be copied unless it is the address of the operator's
Listenarr instance.

## Required path mapping

The mapping translates the path reported by Listenarr into the path mounted inside Silo. For
the deployment example used by this repository, configure this exact mapping:

| Listenarr path | Silo container path |
| --- | --- |
| `/provider/media/audiobooks` | `/mnt/media/audiobooks` |

The destination must be the root of the Silo audiobook library. Preserve Unicode characters and
do not add a trailing slash or `..` path segments. A request whose mapped path is outside the
configured audiobook root is rejected before scanning.

## Request lifecycle

Requests are forwarded only after Silo approval. The request page exposes the external state as
`queued`, `downloading`, `imported`, `scanning`, `completed`, or `failed`, plus safe failure
detail and provider references when available. Silo marks the request completed only after the
Listenarr import has moved, the mapped folder has been scanned, and an audiobook record is found.
The final Silo audiobook link appears only after that lookup succeeds. A missing link remains a
retryable/actionable failure and is not displayed as completed.

## Source references

- [Docker deployment guide](../deployment/docker.md)
- [Media folder and naming](media-folder-and-naming.md)
- [Silo scan API](../../scan-api.md)
