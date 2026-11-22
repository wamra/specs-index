---
type: brd
version: 0.3
---
# Document portal business requirements

## BRD-001 Document portal platform
Status: approved    Priority: P0

The portal gives teams one place to store, find and share documents.

## BRD-002 Document upload
Status: approved    Priority: P0
Parent: BRD-001

Users can upload a document into a folder from the browser.

## BRD-003 Bulk upload
Status: approved    Priority: P1
Parent: BRD-002
Depends on: BRD-002

Users can upload many documents at once by dropping a folder.

## BRD-004 Upload resume
Status: draft    Priority: P2
Parent: BRD-003

An interrupted bulk upload can be resumed without sending finished files again.

## BRD-005 Document search
Status: approved    Priority: P0
Parent: BRD-001

Users can find documents by searching the portal.

## BRD-006 Full-text search
Status: approved    Priority: P0
Parent: BRD-005

Search matches words in titles and document text and ranks exact title matches first.

## BRD-007 Search filters
Status: approved    Priority: P1
Parent: BRD-005
Depends on: BRD-006

Search results can be narrowed by status, owner and date.

## BRD-008 Version history
Status: approved    Priority: P1
Parent: BRD-001

Every saved change to a document is kept as a numbered version.

## BRD-009 Version restore
Status: draft    Priority: P2
Parent: BRD-008
Depends on: BRD-008

An editor can restore an earlier version, which becomes the newest version.

## BRD-010 Access control
Status: approved    Priority: P0
Parent: BRD-001

Access to documents is limited to people who are allowed to see them.

## BRD-011 Single sign-on
Status: approved    Priority: P0
Parent: BRD-010

Employees sign in once through the corporate identity provider.

## BRD-012 Role-based permissions
Status: approved    Priority: P0
Parent: BRD-010

Permissions are granted by role to groups, not to individual files. See BRD-011 for how identities are established.

## BRD-013 Share links
Status: approved    Priority: P1
Parent: BRD-010
Conflicts with: BRD-014

Owners can create share links that give outside people read access until an expiry date.

## BRD-014 Disable external sharing
Status: approved    Priority: P1
Parent: BRD-010

Administrators can disable external sharing for a whole workspace.

## BRD-015 Audit log
Status: draft    Priority: P1
Parent: BRD-001

Every view, edit and permission change is recorded in an audit log.

## BRD-016 Retention policies
Status: approved    Priority: P1
Parent: BRD-001

Documents are archived or deleted automatically when their retention period ends.

## BRD-017 Legal hold
Status: draft    Priority: P2
Parent: BRD-016
Depends on: BRD-016

A legal hold suspends deletion of the held documents until it is lifted.

## BRD-018 Document preview
Status: approved    Priority: P1
Parent: BRD-001

Documents render as page images in the browser without downloading. Rendering reuses the pipeline described by `FDS-010`.

## BRD-019 Preview annotations
Status: draft    Priority: P3
Parent: BRD-018

Reviewers can draw comments and highlights on a preview.

## BRD-020 Notifications
Status: approved    Priority: P1
Parent: BRD-001

Watchers are told when a document they follow changes.

```text
Example payload, not a requirement:
BRD-777 changed, see FDS-777
## BRD-888 Example heading inside a code block
```

## BRD-021 Email digest
Status: draft    Priority: P2
Parent: BRD-020

Watchers can choose a daily email digest instead of instant messages.

## BRD-022 Export
Status: approved    Priority: P1
Parent: BRD-001

Users can export a folder with its version history for use outside the portal.

## BRD-023 Legacy fax intake
Status: deprecated    Priority: P3
Parent: BRD-001

Paper documents received by fax are filed automatically. This capability is retired.

## BRD-024 Accessibility
Status: approved    Priority: P1
Parent: BRD-001

The portal meets WCAG 2.2 level AA.

## BRD-025 Offline mode
Status: draft    Priority: P2
Parent: BRD-001
Depends on: BRD-099

Users can open recently used documents without a network connection.
