Feature: Preview, notification and export

  @FDS-010 @BRD-018
  Scenario: Render a PDF preview
    Given a PDF document with 12 pages
    When I open the document
    Then the first page image appears within 2 seconds

  @FDS-012 @BRD-020
  Scenario: Notify watchers of a new version
    Given I watch the document "Proposal"
    When a colleague saves a new version
    Then I receive a notification

  @FDS-013 @BRD-022 @smoke
  Scenario: Export a folder as an archive
    Given a folder with 5 documents
    When I export the folder
    Then I download an archive with 5 documents and a checksum file
