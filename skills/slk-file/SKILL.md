---
name: slk-file
description: "List and manage files"
metadata:
  version: 0.8.1
  openclaw:
    category: "productivity"
    requires:
      bins:
        - slk
    cliHelp: "slk file --help"
---

# slk file

List and manage files

> **PREREQUISITE:** Read `../slk-shared/SKILL.md` for auth, global flags, security rules, and exit codes. If missing, run `slk generate-skills`.

| Command | Description |
|---------|-------------|
| `slk file delete` | Delete a file |
| `slk file download` | Download an authenticated Slack file to a private local path |
| `slk file info` | Show file details |
| `slk file list` | List files |
| `slk file public` | Make a file publicly accessible |
| `slk file revoke-public` | Revoke a file's public link |
| `slk file upload` | Upload files, optionally sharing them to a channel or DM |

## slk file delete

Delete a file

**Slack API:** `files.delete`

```bash
slk file delete [flags]
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--file` | ✓ | — | file ID |

> [!CAUTION]
> Write command — confirm with the user before executing; preview with `--dry-run`.

## slk file download

Download an authenticated Slack file to a private local path

**Slack API:** `files.info`

```bash
slk file download [flags]
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--file` | — | — | Slack file ID |
| `--max-bytes` | — | `26214400` | maximum downloaded bytes (must be positive) |
| `--output` | — | — | destination file path; existing targets and file symlinks are rejected |
| `--url` | — | — | known HTTPS files.slack.com protected image URL (alternative to --file) |

**Tips:** Download a Slack file without printing binary data. With no --output, create a private temporary directory and print the absolute path. The downloaded file remains until you explicitly remove it. --url accepts only a known files.slack.com image URL; URLs and credentials are never printed.

## slk file info

Show file details

**Slack API:** `files.info`

```bash
slk file info [flags]
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--file` | ✓ | — | file ID |

**Tips:** Show file metadata, the best available URL, and whether that URL requires Slack authentication. No file bytes are fetched or saved.

## slk file list

List files

**Slack API:** `files.list`

```bash
slk file list [flags]
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--channel` | — | — | filter by channel ID (optional) |
| `--types` | — | — | filter by file types, comma-separated (optional) |
| `--user` | — | — | filter by user ID (optional) |

## slk file public

Make a file publicly accessible

**Slack API:** `files.sharedPublicURL`

```bash
slk file public [flags]
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--file` | ✓ | — | file ID |

**Tips:** Makes the file accessible to anyone with the link.

> [!CAUTION]
> Write command — confirm with the user before executing; preview with `--dry-run`.

## slk file revoke-public

Revoke a file's public link

**Slack API:** `files.revokePublicURL`

```bash
slk file revoke-public [flags]
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--file` | ✓ | — | file ID |

> [!CAUTION]
> Write command — confirm with the user before executing; preview with `--dry-run`.

## slk file upload

Upload files, optionally sharing them to a channel or DM

**Slack API:** `files.getUploadURLExternal`

```bash
slk file upload [flags]
```

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--alt-text` | — | — | image description applied to each uploaded file |
| `--channel` | — | — | channel ID or user ID for a DM (omit for a private upload) |
| `--file` | ✓ | `[]` | path to file to upload (repeatable) |
| `--text` | — | — | initial comment when sharing files |
| `--text-file` | — | — | read initial comment from a file ('-' for stdin) |
| `--thread` | — | — | share files in this thread (requires --channel) |
| `--title` | — | — | file title (single file only; defaults to filename) |

**Tips:** Upload nonempty, readable regular files using Slack's hosted upload flow. Repeat --file to upload multiple files and share them together in one completion. --title is supported only with a single file; otherwise each title defaults to its filename. --alt-text applies the same image description to every file.

> [!CAUTION]
> Write command — confirm with the user before executing; preview with `--dry-run`.


